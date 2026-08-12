package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// minPasswordLen is the enforced floor for user-chosen passwords (bootstrap and
// change-password). Aligns with ADR-0006's move to real user credentials.
const minPasswordLen = 12

// Handler serves /api/auth/*.
type Handler struct {
	Sessions *Sessions
	Store    store.Store
	Hasher   *PasswordHasher
	Log      *slog.Logger
	Limiter  *LoginLimiter // nil disables rate limiting (tests)
}

func unauthenticated() *types.APIError {
	return &types.APIError{
		Code:    "unauthenticated",
		Message: "Invalid email or password.",
		Status:  http.StatusUnauthorized,
	}
}

// identityCtxKey keys the authenticated Identity in the request context.
type identityCtxKey struct{}

// IdentityFrom returns the authenticated Identity set by Authenticate, if any.
// Exported so the handlers package can read the principal in later phases.
func IdentityFrom(ctx context.Context) (*Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(*Identity)
	return id, ok
}

// Authenticate rejects requests without a valid session and injects the
// resolved *Identity into the request context. It replaces the old
// RequireSession on the protected route group.
func (h *Handler) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := h.Sessions.Verify(r.Context(), r)
		if err != nil {
			writeErr(w, &types.APIError{Code: "unauthenticated", Message: "Not signed in.", Status: http.StatusUnauthorized})
			return
		}
		ctx := context.WithValue(r.Context(), identityCtxKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// BootstrapStatus handles GET /api/auth/bootstrap-status. Public.
func (h *Handler) BootstrapStatus(w http.ResponseWriter, r *http.Request) {
	n, err := h.Store.CountUsers(r.Context())
	if err != nil {
		h.logger().Error("bootstrap-status count users", "err", err)
		writeErr(w, &types.APIError{Code: "internal", Message: "internal server error", Status: http.StatusInternalServerError})
		return
	}
	writeJSON(w, http.StatusOK, types.BootstrapStatus{NeedsBootstrap: n == 0})
}

// Bootstrap handles POST /api/auth/bootstrap. Public but hard-guarded: succeeds
// only when zero users exist (409 otherwise). Creates the first user
// (platform-admin) + an Owner membership on the default tenant, then signs in.
func (h *Handler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	var req types.BootstrapRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, &types.APIError{Code: "invalid_request", Message: "Request body must be JSON with email, password, displayName.", Status: http.StatusBadRequest})
		return
	}
	ip := clientIP(r)
	if h.Limiter != nil && !h.Limiter.Allow(ip) {
		h.logger().Warn("bootstrap rate limited", "ip", ip)
		writeErr(w, rateLimited())
		return
	}
	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || !strings.Contains(req.Email, "@") {
		writeErr(w, &types.APIError{Code: "invalid_request", Message: "A valid email is required.", Status: http.StatusBadRequest})
		return
	}
	if err := validatePasswordStrength(req.Password); err != nil {
		writeErr(w, err)
		return
	}

	hash, err := h.hashPassword(req.Password)
	if err != nil {
		h.logger().Error("bootstrap hash", "err", err)
		writeErr(w, internalErr())
		return
	}

	ctx := r.Context()
	var user *store.User
	err = h.Store.WithTx(ctx, func(s store.Store) error {
		u, err := createFirstAdmin(ctx, s, req.Email, strings.TrimSpace(req.DisplayName), hash, AlgoArgon2id)
		if err != nil {
			return err
		}
		user = u
		return nil
	})
	if errors.Is(err, errAlreadyBootstrapped) {
		writeErr(w, &types.APIError{Code: "conflict", Message: "Proxcloud has already been set up.", Status: http.StatusConflict})
		return
	}
	if err != nil {
		h.logger().Error("bootstrap create admin", "err", err)
		writeErr(w, internalErr())
		return
	}

	cookie, err := h.Sessions.Issue(ctx, user.ID, r)
	if err != nil {
		h.logger().Error("bootstrap issue session", "err", err)
		writeErr(w, internalErr())
		return
	}
	if h.Limiter != nil {
		h.Limiter.Reset(ip)
	}
	http.SetCookie(w, cookie)
	h.logger().Info("bootstrap ok", "email", req.Email)
	w.WriteHeader(http.StatusNoContent)
}

// Login handles POST /api/auth/login. Looks a user up by lower(email), verifies
// Argon2id/bcrypt, transparently rehashes bcrypt→Argon2id, and issues a fresh
// DB session. Unknown-email and bad-password return the same 401, and both run
// a hash so timing does not reveal which emails exist.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req types.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, &types.APIError{Code: "invalid_request", Message: "Request body must be JSON with email and password.", Status: http.StatusBadRequest})
		return
	}

	ip := clientIP(r)
	if h.Limiter != nil && !h.Limiter.Allow(ip) {
		h.logger().Warn("login rate limited", "ip", ip)
		writeErr(w, rateLimited())
		return
	}

	ctx := r.Context()
	user, lookupErr := h.Store.GetUserByEmail(ctx, req.Email)

	// Bound concurrent memory-hard hashing so a flood cannot exhaust CPU/RAM.
	if h.Limiter != nil {
		release := h.Limiter.AcquireBcrypt()
		defer release()
	}

	var ok, needsRehash bool
	if lookupErr == nil && user.PasswordHash != nil {
		ok, needsRehash = h.Hasher.Verify(req.Password, *user.PasswordHash, algoOf(user))
	} else {
		// Unknown email (or credential-less user): still spend hashing time.
		h.Hasher.VerifyDummy(req.Password)
	}

	if !ok || user == nil || user.Disabled {
		h.logger().Warn("login failed", "ip", ip)
		writeErr(w, unauthenticated())
		return
	}

	if needsRehash {
		if nh, err := h.Hasher.Hash(req.Password); err == nil {
			if err := h.Store.UpdatePasswordHash(ctx, user.ID, nh, AlgoArgon2id); err != nil {
				h.logger().Warn("password rehash failed", "err", err) // non-fatal
			}
		}
	}

	// Session rotation (ADR-0006): if this browser already carried a valid
	// session for the SAME user, retire it once the new one is issued so a
	// re-login does not leave the previous token live.
	oldSessionID := ""
	if prev, verr := h.Sessions.Verify(ctx, r); verr == nil && prev.UserID == user.ID {
		oldSessionID = prev.SessionID
	}

	cookie, err := h.Sessions.Issue(ctx, user.ID, r)
	if err != nil {
		h.logger().Error("login issue session", "err", err)
		writeErr(w, internalErr())
		return
	}
	if h.Limiter != nil {
		h.Limiter.Reset(ip)
	}
	http.SetCookie(w, cookie)
	if oldSessionID != "" {
		if err := h.Sessions.Revoke(ctx, oldSessionID); err != nil {
			h.logger().Warn("login rotate: revoke prior session", "err", err) // non-fatal
		}
	}
	h.logger().Info("login ok", "user_id", user.ID)
	w.WriteHeader(http.StatusNoContent)
}

// Logout handles POST /api/auth/logout. Revokes the caller's current session
// server-side and clears the cookie. Authenticated.
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if id, ok := IdentityFrom(r.Context()); ok {
		if err := h.Sessions.Revoke(r.Context(), id.SessionID); err != nil {
			h.logger().Warn("logout revoke", "err", err)
		}
	}
	http.SetCookie(w, h.Sessions.Clear())
	w.WriteHeader(http.StatusNoContent)
}

// Me handles GET /api/auth/me. Authenticated.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeErr(w, &types.APIError{Code: "unauthenticated", Message: "Not signed in.", Status: http.StatusUnauthorized})
		return
	}
	user, err := h.Store.GetUserByID(r.Context(), id.UserID)
	if err != nil {
		h.logger().Error("me get user", "err", err)
		writeErr(w, internalErr())
		return
	}
	writeJSON(w, http.StatusOK, types.Me{
		ID:              user.ID,
		Email:           user.Email,
		DisplayName:     user.DisplayName,
		IsPlatformAdmin: user.IsPlatformAdmin,
		TOTPEnabled:     user.TOTPEnabled,
	})
}

// ChangePassword handles POST /api/auth/password. Re-verifies the current
// password, validates the new one, rehashes to Argon2id, and revokes all of the
// user's OTHER sessions (keeps the current one) as a fixation defense.
func (h *Handler) ChangePassword(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeErr(w, &types.APIError{Code: "unauthenticated", Message: "Not signed in.", Status: http.StatusUnauthorized})
		return
	}
	var req types.ChangePasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, &types.APIError{Code: "invalid_request", Message: "Request body must be JSON with currentPassword and newPassword.", Status: http.StatusBadRequest})
		return
	}
	ip := clientIP(r)
	if h.Limiter != nil && !h.Limiter.Allow(ip) {
		h.logger().Warn("password change rate limited", "ip", ip)
		writeErr(w, rateLimited())
		return
	}
	if err := validatePasswordStrength(req.NewPassword); err != nil {
		writeErr(w, err)
		return
	}

	ctx := r.Context()
	user, err := h.Store.GetUserByID(ctx, id.UserID)
	if err != nil {
		h.logger().Error("password get user", "err", err)
		writeErr(w, internalErr())
		return
	}

	if h.Limiter != nil {
		release := h.Limiter.AcquireBcrypt()
		defer release()
	}
	verified := user.PasswordHash != nil
	if verified {
		verified, _ = h.Hasher.Verify(req.CurrentPassword, *user.PasswordHash, algoOf(user))
	} else {
		h.Hasher.VerifyDummy(req.CurrentPassword)
	}
	if !verified {
		writeErr(w, &types.APIError{Code: "unauthenticated", Message: "Current password is incorrect.", Status: http.StatusUnauthorized})
		return
	}

	nh, err := h.Hasher.Hash(req.NewPassword)
	if err != nil {
		h.logger().Error("password hash", "err", err)
		writeErr(w, internalErr())
		return
	}
	if err := h.Store.UpdatePasswordHash(ctx, user.ID, nh, AlgoArgon2id); err != nil {
		h.logger().Error("password update", "err", err)
		writeErr(w, internalErr())
		return
	}
	if err := h.Store.RevokeOtherUserSessions(ctx, user.ID, id.SessionID); err != nil {
		h.logger().Warn("password revoke others", "err", err) // non-fatal
	}
	if h.Limiter != nil {
		h.Limiter.Reset(ip)
	}
	h.logger().Info("password changed", "user_id", user.ID)
	w.WriteHeader(http.StatusNoContent)
}

// ListSessions handles GET /api/auth/sessions — the caller's own live sessions.
func (h *Handler) ListSessions(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeErr(w, &types.APIError{Code: "unauthenticated", Message: "Not signed in.", Status: http.StatusUnauthorized})
		return
	}
	sessions, err := h.Store.ListSessionsByUser(r.Context(), id.UserID)
	if err != nil {
		h.logger().Error("list sessions", "err", err)
		writeErr(w, internalErr())
		return
	}
	out := make([]types.SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		if !h.Sessions.Live(s) {
			continue // hide idle-expired rows the cookie could no longer use
		}
		out = append(out, types.SessionInfo{
			ID:         s.ID,
			CreatedAt:  s.CreatedAt,
			LastSeenAt: s.LastSeenAt,
			IP:         deref(s.IP),
			UserAgent:  deref(s.UserAgent),
			Current:    s.ID == id.SessionID,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// DeleteSession handles DELETE /api/auth/sessions/{id} — revokes one of the
// CALLER'S OWN sessions. 404 (not 403) if the id is not theirs — no cross-user
// revoke and no existence leak.
func (h *Handler) DeleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeErr(w, &types.APIError{Code: "unauthenticated", Message: "Not signed in.", Status: http.StatusUnauthorized})
		return
	}
	target := chi.URLParam(r, "id")
	sessions, err := h.Store.ListSessionsByUser(r.Context(), id.UserID)
	if err != nil {
		h.logger().Error("delete session list", "err", err)
		writeErr(w, internalErr())
		return
	}
	found := false
	for _, s := range sessions {
		if s.ID == target && h.Sessions.Live(s) {
			found = true
			break
		}
	}
	if !found {
		writeErr(w, &types.APIError{Code: "not_found", Message: "Session not found.", Status: http.StatusNotFound})
		return
	}
	if err := h.Sessions.Revoke(r.Context(), target); err != nil {
		h.logger().Error("delete session revoke", "err", err)
		writeErr(w, internalErr())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- helpers ---

func (h *Handler) logger() *slog.Logger {
	if h.Log != nil {
		return h.Log
	}
	return slog.Default()
}

// hashPassword bounds concurrent memory-hard hashing behind the shared
// semaphore (nil Limiter → unbounded, as in tests), releasing the slot as soon
// as the hash returns. Used by the public Bootstrap path, which — unlike Login
// and ChangePassword — does not otherwise hold the semaphore, so an unbounded
// bootstrap flood cannot exhaust CPU/RAM on Argon2id work.
func (h *Handler) hashPassword(pw string) (string, error) {
	if h.Limiter != nil {
		release := h.Limiter.AcquireBcrypt()
		defer release()
	}
	return h.Hasher.Hash(pw)
}

// algoOf returns the user's password algorithm, defaulting to Argon2id.
func algoOf(u *store.User) string {
	if u.PasswordAlgo != nil && *u.PasswordAlgo != "" {
		return *u.PasswordAlgo
	}
	return AlgoArgon2id
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func validatePasswordStrength(pw string) *types.APIError {
	if len([]rune(pw)) < minPasswordLen {
		return &types.APIError{
			Code:    "invalid_request",
			Message: "Password must be at least 12 characters.",
			Status:  http.StatusBadRequest,
		}
	}
	return nil
}

func rateLimited() *types.APIError {
	return &types.APIError{Code: "rate_limited", Message: "Too many attempts — wait a minute and try again.", Status: http.StatusTooManyRequests}
}

func internalErr() *types.APIError {
	return &types.APIError{Code: "internal", Message: "internal server error", Status: http.StatusInternalServerError}
}

// Local JSON helpers (duplicated from httpserver to avoid an import cycle).
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, e *types.APIError) {
	writeJSON(w, e.Status, types.ErrorEnvelope{Error: *e})
}
