package types

import "time"

// LoginRequest is the POST /api/auth/login body.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// BootstrapStatus is the GET /api/auth/bootstrap-status response.
type BootstrapStatus struct {
	// NeedsBootstrap is true iff zero users exist (first-run).
	NeedsBootstrap bool `json:"needsBootstrap"`
}

// BootstrapRequest is the POST /api/auth/bootstrap body (first-run only).
type BootstrapRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"displayName"`
}

// ChangePasswordRequest is the POST /api/auth/password body.
type ChangePasswordRequest struct {
	CurrentPassword string `json:"currentPassword"`
	NewPassword     string `json:"newPassword"`
}

// Me is the GET /api/auth/me response — the signed-in principal.
type Me struct {
	ID              string `json:"id"`
	Email           string `json:"email"`
	DisplayName     string `json:"displayName"`
	IsPlatformAdmin bool   `json:"isPlatformAdmin"`
	TOTPEnabled     bool   `json:"totpEnabled"`
}

// SessionInfo is one entry in GET /api/auth/sessions — a live server-side
// session belonging to the caller.
type SessionInfo struct {
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	IP         string    `json:"ip"`
	UserAgent  string    `json:"userAgent"`
	// Current marks the session backing the requesting cookie.
	Current bool `json:"current"`
}
