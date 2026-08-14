package auth

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

func newTestHandler(t *testing.T) (*Handler, *fakeStore) {
	t.Helper()
	fs := newFakeStore()
	return &Handler{
		Sessions: NewSessions(fs, false, false, time.Hour, 24*time.Hour),
		Store:    fs,
		Hasher:   NewHasher(),
		Log:      slog.New(slog.NewTextHandler(testWriter{t}, nil)),
		Limiter:  NewLoginLimiter(),
	}, fs
}

// seedUser inserts a user with an Argon2id password directly through the store.
func seedUser(t *testing.T, h *Handler, fs *fakeStore, email, pw string, admin bool) *store.User {
	t.Helper()
	hash, err := h.Hasher.Hash(pw)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	u, err := fs.CreateUser(context.Background(), store.CreateUserParams{
		Email: email, DisplayName: "Test", PasswordHash: hash, PasswordAlgo: AlgoArgon2id, IsPlatformAdmin: admin,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	return u
}

func issueCookie(t *testing.T, h *Handler, userID string) *http.Cookie {
	t.Helper()
	c, err := h.Sessions.Issue(context.Background(), userID, httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	return c
}

// authedRouter mirrors the production authenticated group so tests exercise the
// real Authenticate middleware and chi URL params.
func authedRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Group(func(r chi.Router) {
		r.Use(h.Authenticate)
		r.Post("/api/auth/logout", h.Logout)
		r.Get("/api/auth/me", h.Me)
		r.Post("/api/auth/password", h.ChangePassword)
		r.Get("/api/auth/sessions", h.ListSessions)
		r.Delete("/api/auth/sessions/{id}", h.DeleteSession)
	})
	return r
}

// --- bootstrap ---

func TestBootstrapLifecycle(t *testing.T) {
	h, _ := newTestHandler(t)

	// bootstrap-status: fresh DB needs bootstrap.
	rec := httptest.NewRecorder()
	h.BootstrapStatus(rec, httptest.NewRequest(http.MethodGet, "/api/auth/bootstrap-status", nil))
	var bs types.BootstrapStatus
	if err := json.Unmarshal(rec.Body.Bytes(), &bs); err != nil || !bs.NeedsBootstrap {
		t.Fatalf("needsBootstrap = %+v (err %v), want true", bs, err)
	}

	// weak password rejected.
	rec = httptest.NewRecorder()
	h.Bootstrap(rec, jsonReq(http.MethodPost, "/api/auth/bootstrap", `{"email":"a@b.com","password":"short","displayName":"A"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("weak password status = %d, want 400", rec.Code)
	}

	// valid bootstrap → 204 + cookie, creates a platform admin + owner membership.
	rec = httptest.NewRecorder()
	h.Bootstrap(rec, jsonReq(http.MethodPost, "/api/auth/bootstrap", `{"email":"admin@b.com","password":"a-strong-passphrase","displayName":"Admin"}`))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("bootstrap status = %d, want 204 (%s)", rec.Code, rec.Body)
	}
	if !hasSessionCookie(rec) {
		t.Fatal("bootstrap did not set a session cookie")
	}
	u, err := h.Store.GetUserByEmail(context.Background(), "admin@b.com")
	if err != nil {
		t.Fatalf("GetUserByEmail after bootstrap: %v", err)
	}
	if !u.IsPlatformAdmin {
		t.Fatal("bootstrapped user is not platform admin")
	}
	mems, _ := h.Store.ListMembershipsByUser(context.Background(), u.ID)
	if len(mems) != 1 || mems[0].Role != "owner" || mems[0].ScopeType != "tenant" {
		t.Fatalf("membership = %+v, want single tenant owner", mems)
	}

	// bootstrap-status now false.
	rec = httptest.NewRecorder()
	h.BootstrapStatus(rec, httptest.NewRequest(http.MethodGet, "/api/auth/bootstrap-status", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &bs)
	if bs.NeedsBootstrap {
		t.Fatal("needsBootstrap still true after bootstrap")
	}

	// second bootstrap → 409 conflict.
	rec = httptest.NewRecorder()
	h.Bootstrap(rec, jsonReq(http.MethodPost, "/api/auth/bootstrap", `{"email":"other@b.com","password":"another-strong-pass","displayName":"X"}`))
	if rec.Code != http.StatusConflict {
		t.Fatalf("second bootstrap status = %d, want 409", rec.Code)
	}
}

// --- login ---

func TestLogin(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantCookie bool
		wantCode   string
	}{
		{"valid", `{"email":"user@b.com","password":"correct-horse-battery"}`, http.StatusOK, true, ""},
		{"wrong password", `{"email":"user@b.com","password":"wrong"}`, http.StatusUnauthorized, false, "unauthenticated"},
		{"unknown email", `{"email":"nobody@b.com","password":"correct-horse-battery"}`, http.StatusUnauthorized, false, "unauthenticated"},
		{"malformed json", `{not json`, http.StatusBadRequest, false, "invalid_request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fs := newTestHandler(t)
			seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)

			rec := httptest.NewRecorder()
			h.Login(rec, jsonReq(http.MethodPost, "/api/auth/login", tt.body))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body)
			}
			if hasSessionCookie(rec) != tt.wantCookie {
				t.Fatalf("cookie present = %v, want %v", hasSessionCookie(rec), tt.wantCookie)
			}
			if tt.wantCode != "" {
				var env types.ErrorEnvelope
				_ = json.Unmarshal(rec.Body.Bytes(), &env)
				if env.Error.Code != tt.wantCode {
					t.Errorf("code = %q, want %q", env.Error.Code, tt.wantCode)
				}
			}
		})
	}
}

// TestLoginCookieSecureFromForwardedProto locks the prod Mode-A fix: behind a
// trusted proxy on a plain-HTTP hop (Caddy :80, TLS at Cloudflare), login must
// still set a Secure proxcloud_session because the EXTERNAL connection is HTTPS,
// signalled by X-Forwarded-Proto. It also proves the trust is gated: with the
// proxy trust OFF, a client-supplied X-Forwarded-Proto can NOT flip the cookie
// (no spoofing the scheme up or down).
func TestLoginCookieSecureFromForwardedProto(t *testing.T) {
	loginReq := func(xfp string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "http://guest/api/auth/login",
			strings.NewReader(`{"email":"user@b.com","password":"correct-horse-battery"}`))
		r.Header.Set("Content-Type", "application/json")
		if xfp != "" {
			r.Header.Set("X-Forwarded-Proto", xfp)
		}
		return r // httptest.NewRequest with http:// => r.TLS == nil (the plain hop)
	}
	sessionCookie := func(rec *httptest.ResponseRecorder) *http.Cookie {
		for _, c := range rec.Result().Cookies() {
			if c.Name == CookieName {
				return c
			}
		}
		return nil
	}

	cases := []struct {
		name                   string
		trustProxy, secureBase bool
		xfp                    string
		wantSecure             bool
	}{
		{"trusted proxy + XFP=https → Secure (Mode A HTTPS user)", true, false, "https", true},
		{"trusted proxy + XFP=http → not Secure (smoke over http)", true, false, "http", false},
		{"trusted proxy + XFP comma-list → first hop wins", true, false, "https, http", true},
		{"trusted proxy + no XFP → base fallback (fail-safe Secure)", true, true, "", true},
		{"UNtrusted proxy IGNORES XFP=https (no spoof-up)", false, false, "https", false},
		{"UNtrusted proxy: base Secure holds despite XFP=http", false, true, "http", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := newFakeStore()
			h := &Handler{
				Sessions: NewSessions(fs, tc.secureBase, tc.trustProxy, time.Hour, 24*time.Hour),
				Store:    fs,
				Hasher:   NewHasher(),
				Log:      slog.New(slog.NewTextHandler(testWriter{t}, nil)),
				Limiter:  NewLoginLimiter(),
			}
			seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)

			rec := httptest.NewRecorder()
			h.Login(rec, loginReq(tc.xfp))
			if rec.Code != http.StatusOK {
				t.Fatalf("login status = %d, want 200 (%s)", rec.Code, rec.Body)
			}
			c := sessionCookie(rec)
			if c == nil {
				t.Fatal("login set no proxcloud_session cookie")
			}
			if c.Secure != tc.wantSecure {
				t.Fatalf("cookie Secure = %v, want %v", c.Secure, tc.wantSecure)
			}
		})
	}
}

// TestPasswordLengthBounds is the H4 amplifier regression: passwords have both a
// floor and a ceiling, so Argon2id never hashes an attacker-sized input.
func TestPasswordLengthBounds(t *testing.T) {
	if validatePasswordStrength("short") == nil {
		t.Fatal("too-short password accepted")
	}
	if validatePasswordStrength(strings.Repeat("a", maxPasswordBytes+1)) == nil {
		t.Fatal("over-long password accepted (Argon2id DoS amplifier)")
	}
	if err := validatePasswordStrength("correct-horse-battery"); err != nil {
		t.Fatalf("valid password rejected: %v", err)
	}
}

func TestLoginRateLimited(t *testing.T) {
	h, fs := newTestHandler(t)
	seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	body := `{"email":"user@b.com","password":"wrong"}`

	for i := 0; i < loginMaxPerIP; i++ {
		rec := httptest.NewRecorder()
		h.Login(rec, jsonReq(http.MethodPost, "/api/auth/login", body))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d status = %d, want 401", i, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	h.Login(rec, jsonReq(http.MethodPost, "/api/auth/login", body))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("rate-limited attempt status = %d, want 429", rec.Code)
	}
}

func TestLoginRehashesBcryptToArgon2id(t *testing.T) {
	h, fs := newTestHandler(t)
	bhash, err := ResolveHash("", "legacy-password-123")
	if err != nil {
		t.Fatalf("ResolveHash: %v", err)
	}
	u, err := fs.CreateUser(context.Background(), store.CreateUserParams{
		Email: "legacy@b.com", DisplayName: "Legacy", PasswordHash: bhash, PasswordAlgo: AlgoBcrypt, IsPlatformAdmin: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	rec := httptest.NewRecorder()
	h.Login(rec, jsonReq(http.MethodPost, "/api/auth/login", `{"email":"legacy@b.com","password":"legacy-password-123"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	got, _ := fs.GetUserByID(context.Background(), u.ID)
	if got.PasswordAlgo == nil || *got.PasswordAlgo != AlgoArgon2id {
		t.Fatalf("algo after login = %v, want argon2id (rehash)", got.PasswordAlgo)
	}
	if got.PasswordHash == nil || !strings.HasPrefix(*got.PasswordHash, "$argon2id$") {
		t.Fatal("hash not upgraded to argon2id PHC form")
	}
}

// --- authenticated: me / logout ---

func TestMeAndLogout(t *testing.T) {
	h, fs := newTestHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", true)
	cookie := issueCookie(t, h, u.ID)
	r := authedRouter(h)

	// me signed in.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/me", nil)
	req.AddCookie(cookie)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("me status = %d (%s)", rec.Code, rec.Body)
	}
	var me types.Me
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatalf("me body: %v", err)
	}
	if me.Email != "user@b.com" || !me.IsPlatformAdmin || me.ID != u.ID {
		t.Fatalf("me = %+v, unexpected", me)
	}

	// me without cookie → 401.
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/auth/me", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("me without session = %d, want 401", rec.Code)
	}

	// logout revokes the session server-side.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(cookie)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", rec.Code)
	}
	// the same cookie is now rejected.
	if _, err := h.Sessions.Verify(context.Background(), withCookie(cookie)); err == nil {
		t.Fatal("session still valid after logout — not revoked")
	}
}

// --- change password ---

func TestChangePassword(t *testing.T) {
	h, fs := newTestHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "original-password-1", false)
	current := issueCookie(t, h, u.ID)
	other := issueCookie(t, h, u.ID)
	r := authedRouter(h)

	// weak new password rejected.
	rec := postAuthed(r, "/api/auth/password", current, `{"currentPassword":"original-password-1","newPassword":"short"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("weak new password status = %d, want 400", rec.Code)
	}

	// wrong current password rejected.
	rec = postAuthed(r, "/api/auth/password", current, `{"currentPassword":"nope","newPassword":"a-brand-new-passphrase"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current status = %d, want 401", rec.Code)
	}

	// valid change → 204, revokes OTHER sessions, keeps current.
	rec = postAuthed(r, "/api/auth/password", current, `{"currentPassword":"original-password-1","newPassword":"a-brand-new-passphrase"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("change status = %d, want 204 (%s)", rec.Code, rec.Body)
	}
	if _, err := h.Sessions.Verify(context.Background(), withCookie(current)); err != nil {
		t.Fatal("current session was revoked — should be kept")
	}
	if _, err := h.Sessions.Verify(context.Background(), withCookie(other)); err == nil {
		t.Fatal("other session not revoked after password change")
	}

	// old password no longer works; new one does.
	if ok, _ := h.Hasher.Verify("original-password-1", *mustUser(t, fs, u.ID).PasswordHash, AlgoArgon2id); ok {
		t.Fatal("old password still verifies")
	}
	if ok, _ := h.Hasher.Verify("a-brand-new-passphrase", *mustUser(t, fs, u.ID).PasswordHash, AlgoArgon2id); !ok {
		t.Fatal("new password does not verify")
	}
}

// --- sessions list + delete ---

func TestSessionsListAndDelete(t *testing.T) {
	h, fs := newTestHandler(t)
	alice := seedUser(t, h, fs, "alice@b.com", "alice-password-12", false)
	bob := seedUser(t, h, fs, "bob@b.com", "bob-password-1234", false)
	aliceCookie := issueCookie(t, h, alice.ID)
	aliceOther := issueCookie(t, h, alice.ID)
	bobCookie := issueCookie(t, h, bob.ID)
	r := authedRouter(h)

	// alice lists exactly her two sessions, one marked current.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/sessions", nil)
	req.AddCookie(aliceCookie)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d (%s)", rec.Code, rec.Body)
	}
	var sessions []types.SessionInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("list body: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("alice sees %d sessions, want 2", len(sessions))
	}
	currentCount := 0
	var otherID string
	for _, s := range sessions {
		if s.Current {
			currentCount++
		} else {
			otherID = s.ID
		}
	}
	if currentCount != 1 {
		t.Fatalf("current flag count = %d, want 1", currentCount)
	}

	// bob cannot delete alice's session → 404 (not 403, no existence leak).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/auth/sessions/"+otherID, nil)
	req.AddCookie(bobCookie)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-user delete status = %d, want 404", rec.Code)
	}
	if _, err := h.Sessions.Verify(context.Background(), withCookie(aliceOther)); err != nil {
		t.Fatal("alice's other session was revoked by bob — cross-user revoke leaked")
	}

	// alice deletes her own other session → 204.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/api/auth/sessions/"+otherID, nil)
	req.AddCookie(aliceCookie)
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("self delete status = %d, want 204", rec.Code)
	}
	if _, err := h.Sessions.Verify(context.Background(), withCookie(aliceOther)); err == nil {
		t.Fatal("alice's other session still valid after self-delete")
	}
}

// --- session verification edge cases ---

func TestSessionVerify(t *testing.T) {
	base := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		setup   func(fs *fakeStore, s *Sessions, u *store.User) *http.Cookie
		wantErr bool
	}{
		{
			name: "valid",
			setup: func(fs *fakeStore, s *Sessions, u *store.User) *http.Cookie {
				c, _ := s.Issue(context.Background(), u.ID, nil)
				return c
			},
			wantErr: false,
		},
		{
			name: "revoked",
			setup: func(fs *fakeStore, s *Sessions, u *store.User) *http.Cookie {
				c, _ := s.Issue(context.Background(), u.ID, nil)
				sess, _ := fs.GetSessionByTokenHash(context.Background(), hashToken(c.Value))
				_ = fs.RevokeSession(context.Background(), sess.ID)
				return c
			},
			wantErr: true,
		},
		{
			name: "idle expired",
			setup: func(fs *fakeStore, s *Sessions, u *store.User) *http.Cookie {
				c, _ := s.Issue(context.Background(), u.ID, nil)
				sess, _ := fs.GetSessionByTokenHash(context.Background(), hashToken(c.Value))
				_ = fs.TouchSession(context.Background(), sess.ID, base.Add(-2*time.Hour)) // idleTTL is 1h
				return c
			},
			wantErr: true,
		},
		{
			name: "absolute expired",
			setup: func(fs *fakeStore, s *Sessions, u *store.User) *http.Cookie {
				c, _ := s.Issue(context.Background(), u.ID, nil)
				sess, _ := fs.GetSessionByTokenHash(context.Background(), hashToken(c.Value))
				sess.AbsoluteExpiresAt = base.Add(-time.Minute)
				sess.LastSeenAt = base // not idle-expired, but absolute-expired
				fs.sessions[sess.ID] = sess
				return c
			},
			wantErr: true,
		},
		{
			name: "disabled user",
			setup: func(fs *fakeStore, s *Sessions, u *store.User) *http.Cookie {
				c, _ := s.Issue(context.Background(), u.ID, nil)
				fs.users[u.ID].Disabled = true
				return c
			},
			wantErr: true,
		},
		{
			name: "unknown token",
			setup: func(fs *fakeStore, s *Sessions, u *store.User) *http.Cookie {
				return &http.Cookie{Name: CookieName, Value: "not-a-real-token"}
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fs := newFakeStore()
			fs.now = func() time.Time { return base }
			s := NewSessions(fs, false, false, time.Hour, 24*time.Hour)
			s.now = func() time.Time { return base }
			u, _ := fs.CreateUser(context.Background(), store.CreateUserParams{
				Email: "u@b.com", PasswordHash: "x", PasswordAlgo: AlgoArgon2id,
			})
			cookie := tt.setup(fs, s, u)
			_, err := s.Verify(context.Background(), withCookie(cookie))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Verify err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSessionTokenStoredHashedNeverRaw(t *testing.T) {
	fs := newFakeStore()
	s := NewSessions(fs, false, false, time.Hour, 24*time.Hour)
	u, _ := fs.CreateUser(context.Background(), store.CreateUserParams{Email: "u@b.com", PasswordHash: "x", PasswordAlgo: AlgoArgon2id})
	c, err := s.Issue(context.Background(), u.ID, nil)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	for _, sess := range fs.sessions {
		if sess.TokenHash == c.Value {
			t.Fatal("raw token stored in token_hash — must be hashed")
		}
		if sess.TokenHash != hashToken(c.Value) {
			t.Fatal("token_hash is not SHA-256 of the cookie token")
		}
	}
}

func TestResolveHash(t *testing.T) {
	if _, err := ResolveHash("not-bcrypt", ""); err == nil {
		t.Error("invalid hash accepted")
	}
	h, err := ResolveHash("", "pw")
	if err != nil || !CheckPassword(h, "pw") || CheckPassword(h, "other") {
		t.Errorf("plaintext hashing broken: %v", err)
	}
}

// --- helpers ---

func jsonReq(method, target, body string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func postAuthed(r chi.Router, target string, cookie *http.Cookie, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := jsonReq(http.MethodPost, target, body)
	req.AddCookie(cookie)
	r.ServeHTTP(rec, req)
	return rec
}

func withCookie(c *http.Cookie) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(c)
	return req
}

func hasSessionCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName && c.Value != "" && c.MaxAge > 0 {
			return true
		}
	}
	return false
}

func mustUser(t *testing.T, fs *fakeStore, id string) *store.User {
	t.Helper()
	u, err := fs.GetUserByID(context.Background(), id)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	return u
}
