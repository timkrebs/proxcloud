package types

import "time"

// LoginRequest is the POST /api/auth/login body.
type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginResponse is the POST /api/auth/login response — now always 200 with this
// body (contract change from the Phase-2 204). When TotpRequired is true, NO
// session cookie is set; a proxcloud_totp challenge cookie is set instead and the
// caller must complete POST /api/auth/login/totp.
type LoginResponse struct {
	TotpRequired bool `json:"totpRequired"`
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

// Me is the GET /api/auth/me response — the signed-in principal plus the
// tenants they can reach and their active tenant (from the session).
type Me struct {
	ID              string `json:"id"`
	Email           string `json:"email"`
	DisplayName     string `json:"displayName"`
	IsPlatformAdmin bool   `json:"isPlatformAdmin"`
	TOTPEnabled     bool   `json:"totpEnabled"`
	// ActiveTenantId mirrors sessions.active_tenant_id; "" when unset/never chosen.
	ActiveTenantId string `json:"activeTenantId"`
	// RecoveryCodesRemaining is the count of unused TOTP recovery codes (drives
	// the Settings "N codes left" line). 0 when TOTP is disabled. Never lists the
	// codes themselves — those are shown once, at enable/regenerate.
	RecoveryCodesRemaining int `json:"recoveryCodesRemaining"`
	// Tenants is every tenant the caller can reach (ListTenantsForUser).
	Tenants []TenantMembership `json:"tenants"`
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
