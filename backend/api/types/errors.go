package types

// ErrorEnvelope is the single error shape every non-2xx API response uses.
type ErrorEnvelope struct {
	Error APIError `json:"error"`
}

// APIError carries a stable machine code, a safe human message, and — when
// the failure originated in Proxmox — the verbatim Proxmox error message.
type APIError struct {
	// Code is one of: unauthenticated | forbidden | not_found | conflict |
	// invalid_request | rate_limited | proxmox_auth_failed |
	// proxmox_permission_denied | proxmox_unreachable | proxmox_error |
	// agent_unavailable | console_disabled | timeout | internal
	Code       string `json:"code"`
	Message    string `json:"message"`
	PVEMessage string `json:"pveMessage,omitempty"`

	// Status is the HTTP status to respond with; not serialized.
	Status int `json:"-"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }
