// Package auth implements portal authentication: Argon2id/bcrypt password
// verification and Postgres-backed server-side sessions (ADR-0006). The
// browser holds an opaque 256-bit random token in the proxcloud_session
// cookie; only its SHA-256 hash is stored, so a database leak does not expose
// usable session tokens and logout/revocation are real server-side operations.
// The Proxmox token is never involved — this guards the portal itself.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

const (
	// CookieName is the session cookie the browser holds.
	CookieName = "proxcloud_session"
	// ChallengeCookieName carries the interim second-factor login challenge token
	// (ADR-0013 §3). It is scoped to /api/auth and is NEVER accepted by
	// Authenticate/Verify — it grants only the right to finish step two.
	ChallengeCookieName = "proxcloud_totp"
	// tokenBytes is the opaque session token length (256 bits of entropy).
	tokenBytes = 32
	// touchThrottle bounds how often an active session's last_seen_at is
	// written, so a busy session does not issue an UPDATE on every request.
	touchThrottle = time.Minute
)

// errInvalidSession is the single opaque failure for any bad/expired/revoked
// cookie — callers map it to 401 without leaking which check failed.
var errInvalidSession = errors.New("auth: invalid session")

// Identity is the authenticated principal carried in the request context by
// the Authenticate middleware.
type Identity struct {
	UserID          string
	Email           string
	IsPlatformAdmin bool
	SessionID       string

	// Set by the authz chain (Phase 3). The *Identity in context is a
	// per-request pointer, so ResolveTenant/ResolveScope mutate these in place.
	// Declared now as plumbing; the middleware that populates them ships in the
	// next chunk, so they stay zero-valued (and unused) until then. Roles are
	// plain strings ("owner"|"contributor"|"reader"|"") — the ordering/compare
	// type lives in authz so auth never imports authz.
	ActiveTenantID    string // resolveTenant: the request's active tenant
	TenantRole        string // resolveTenant: max tenant-scope role ("" if project-only member)
	ResolvedProjectID string // resolveScope: from {projectId} or {vmid}->ownership ("" = tenant-level)
	EffectiveRole     string // resolveScope: max(TenantRole, projectRole) for this request
}

// Sessions issues, verifies, and revokes Postgres-backed sessions.
type Sessions struct {
	store       store.Store
	secure      bool // cookie Secure attribute (true behind TLS)
	idleTTL     time.Duration
	absoluteTTL time.Duration
	now         func() time.Time
}

// NewSessions constructs a session manager. secure controls the cookie's
// Secure attribute; idleTTL and absoluteTTL bound inactivity and total lifetime.
func NewSessions(st store.Store, secure bool, idleTTL, absoluteTTL time.Duration) *Sessions {
	return &Sessions{store: st, secure: secure, idleTTL: idleTTL, absoluteTTL: absoluteTTL, now: time.Now}
}

// hashToken returns the hex SHA-256 of a raw token; only this is persisted.
func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Issue mints a new session for userID, persists it, and returns the Set-Cookie
// carrying the raw opaque token. Called on login (rotation is inherent — a
// fresh token/row) and on bootstrap.
func (s *Sessions) Issue(ctx context.Context, userID string, r *http.Request) (*http.Cookie, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	ip, ua := requestMeta(r)
	if _, err := s.store.CreateSession(ctx, store.CreateSessionParams{
		UserID:            userID,
		TokenHash:         hashToken(token),
		AbsoluteExpiresAt: s.now().Add(s.absoluteTTL),
		IP:                ip,
		UserAgent:         ua,
	}); err != nil {
		return nil, err
	}
	return &http.Cookie{
		Name:     CookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secure,
		MaxAge:   int(s.absoluteTTL.Seconds()),
	}, nil
}

// IssueChallengeCookie returns the proxcloud_totp cookie carrying the raw
// interim login-challenge token (ADR-0013 §3). It is scoped to /api/auth so it
// never rides along on non-auth requests, and its lifetime matches the stored
// challenge (LOGIN_CHALLENGE_TTL). Verify/Authenticate never read this cookie —
// holding it grants nothing but the right to attempt POST /api/auth/login/totp.
func (s *Sessions) IssueChallengeCookie(token string, ttl time.Duration) *http.Cookie {
	return &http.Cookie{
		Name:     ChallengeCookieName,
		Value:    token,
		Path:     "/api/auth",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secure,
		MaxAge:   int(ttl.Seconds()),
	}
}

// ClearChallengeCookie returns an expired proxcloud_totp cookie that removes the
// interim challenge from the browser (on success, lockout, or expiry).
func (s *Sessions) ClearChallengeCookie() *http.Cookie {
	return &http.Cookie{
		Name:     ChallengeCookieName,
		Value:    "",
		Path:     "/api/auth",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secure,
		MaxAge:   -1,
	}
}

// Clear returns an expired cookie that removes the session from the browser.
// Server-side revocation is done separately (Revoke) so a stolen cookie dies.
func (s *Sessions) Clear() *http.Cookie {
	return &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.secure,
		MaxAge:   -1,
	}
}

// Verify resolves the request's cookie to an Identity, rejecting missing,
// revoked, idle-expired, or absolute-expired sessions (and disabled users)
// with errInvalidSession. last_seen_at is bumped, throttled to once per minute.
func (s *Sessions) Verify(ctx context.Context, r *http.Request) (*Identity, error) {
	c, err := r.Cookie(CookieName)
	if err != nil || c.Value == "" {
		return nil, errInvalidSession
	}
	sess, err := s.store.GetSessionByTokenHash(ctx, hashToken(c.Value))
	if err != nil {
		return nil, errInvalidSession
	}
	now := s.now()
	if sess.RevokedAt != nil ||
		now.After(sess.AbsoluteExpiresAt) ||
		now.After(sess.LastSeenAt.Add(s.idleTTL)) {
		return nil, errInvalidSession
	}
	user, err := s.store.GetUserByID(ctx, sess.UserID)
	if err != nil || user.Disabled {
		return nil, errInvalidSession
	}
	if now.Sub(sess.LastSeenAt) >= touchThrottle {
		// Best-effort; a failed touch never fails the request.
		_ = s.store.TouchSession(ctx, sess.ID, now)
	}
	id := &Identity{
		UserID:          user.ID,
		Email:           user.Email,
		IsPlatformAdmin: user.IsPlatformAdmin,
		SessionID:       sess.ID,
	}
	// Seed the session's active tenant so the flat account/stream surface (Me,
	// SSE scoping) can read it without a URL {tenantId}. The tenant-scoped authz
	// chain overwrites ActiveTenantID from the path param for scoped routes.
	if sess.ActiveTenantID != nil {
		id.ActiveTenantID = *sess.ActiveTenantID
	}
	return id, nil
}

// Live reports whether a stored session is still usable right now — not
// revoked, and past neither its absolute nor its idle deadline. It mirrors the
// checks in Verify so the sessions list and single-session lookups never
// surface a row whose cookie could no longer authenticate; the store cannot
// apply the idle window itself, since idleTTL is an app-layer setting.
func (s *Sessions) Live(sess store.Session) bool {
	now := s.now()
	return sess.RevokedAt == nil &&
		now.Before(sess.AbsoluteExpiresAt) &&
		now.Before(sess.LastSeenAt.Add(s.idleTTL))
}

// Revoke marks a single session revoked server-side (logout).
func (s *Sessions) Revoke(ctx context.Context, sessionID string) error {
	return s.store.RevokeSession(ctx, sessionID)
}

// requestMeta extracts the client IP and User-Agent for session provenance.
func requestMeta(r *http.Request) (ip, ua *string) {
	if r == nil {
		return nil, nil
	}
	if host := clientIP(r); host != "" {
		ip = &host
	}
	if v := r.UserAgent(); v != "" {
		ua = &v
	}
	return ip, ua
}
