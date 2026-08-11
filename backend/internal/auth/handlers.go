package auth

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net/http"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

// Handler serves /api/auth/*.
type Handler struct {
	Sessions     *Sessions
	AdminUser    string
	PasswordHash string
	Log          *slog.Logger
	Limiter      *LoginLimiter // nil disables rate limiting (tests)
}

func unauthenticated() *types.APIError {
	return &types.APIError{
		Code:    "unauthenticated",
		Message: "Invalid username or password.",
		Status:  http.StatusUnauthorized,
	}
}

// Login handles POST /api/auth/login.
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req types.LoginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, &types.APIError{Code: "invalid_request", Message: "Request body must be JSON with username and password.", Status: http.StatusBadRequest})
		return
	}

	ip := clientIP(r)
	if h.Limiter != nil && !h.Limiter.Allow(ip) {
		h.Log.Warn("login rate limited", "ip", ip)
		writeErr(w, &types.APIError{Code: "rate_limited", Message: "Too many login attempts — wait a minute and try again.", Status: http.StatusTooManyRequests})
		return
	}

	userOK := subtle.ConstantTimeCompare([]byte(req.Username), []byte(h.AdminUser)) == 1
	// Always run the bcrypt comparison so response timing doesn't reveal
	// whether the username exists; the semaphore bounds hash CPU under a
	// request flood.
	if h.Limiter != nil {
		release := h.Limiter.AcquireBcrypt()
		defer release()
	}
	passOK := CheckPassword(h.PasswordHash, req.Password)
	if !userOK || !passOK {
		h.Log.Warn("login failed", "user", req.Username, "ip", r.RemoteAddr)
		writeErr(w, unauthenticated())
		return
	}

	if h.Limiter != nil {
		h.Limiter.Reset(ip)
	}
	http.SetCookie(w, h.Sessions.Issue(req.Username))
	h.Log.Info("login ok", "user", req.Username)
	w.WriteHeader(http.StatusNoContent)
}

// Logout handles POST /api/auth/logout.
func (h *Handler) Logout(w http.ResponseWriter, _ *http.Request) {
	http.SetCookie(w, h.Sessions.Clear())
	w.WriteHeader(http.StatusNoContent)
}

// Me handles GET /api/auth/me.
func (h *Handler) Me(w http.ResponseWriter, r *http.Request) {
	user, err := h.Sessions.Verify(r)
	if err != nil {
		writeErr(w, &types.APIError{Code: "unauthenticated", Message: "Not signed in.", Status: http.StatusUnauthorized})
		return
	}
	writeJSON(w, http.StatusOK, types.Me{Username: user})
}

// RequireSession rejects requests without a valid session cookie and
// slides the window: a cookie past half its lifetime is re-issued.
func (h *Handler) RequireSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, refresh, err := h.Sessions.VerifyRefresh(r)
		if err != nil {
			writeErr(w, &types.APIError{Code: "unauthenticated", Message: "Not signed in.", Status: http.StatusUnauthorized})
			return
		}
		if refresh {
			http.SetCookie(w, h.Sessions.Issue(user))
		}
		next.ServeHTTP(w, r)
	})
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
