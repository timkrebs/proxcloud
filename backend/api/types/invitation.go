package types

import "time"

// CreateInvitationRequest is POST /api/tenants/{tenantId}/invitations (Owner).
// scopeType/scopeId select a tenant-scope or project-scope grant; a project
// scope's scopeId must belong to {tenantId}. role ∈ owner|contributor|reader and
// may not exceed the inviter's own effective role (no privilege escalation).
type CreateInvitationRequest struct {
	Email     string `json:"email"`
	ScopeType string `json:"scopeType"` // "tenant" | "project"
	ScopeID   string `json:"scopeId"`   // tenantId (tenant scope) or projectId (project scope)
	Role      string `json:"role"`      // "owner" | "contributor" | "reader"
}

// Invitation is a pending invite as an Owner sees it. It NEVER carries the token
// (the raw token lives only in the emailed accept link; the DB stores its hash).
type Invitation struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	ScopeType  string    `json:"scopeType"`
	ScopeID    string    `json:"scopeId"`
	ScopeLabel string    `json:"scopeLabel"` // resolved tenant/project display name
	Role       string    `json:"role"`
	InvitedBy  string    `json:"invitedBy"` // inviter display name/email; "" if user gone
	ExpiresAt  time.Time `json:"expiresAt"`
	CreatedAt  time.Time `json:"createdAt"`
	Status     string    `json:"status"` // "pending" | "expired" (accepted rows are not listed)
}

// InvitationDetails is the public GET /api/auth/invitations/{token} response.
// Enumeration-safe: an unknown/expired/used token returns 404, never these
// fields. It NEVER echoes the token.
type InvitationDetails struct {
	Email           string    `json:"email"`      // the address the invite was sent to
	TenantName      string    `json:"tenantName"` // tenant the invite grants access to
	ScopeType       string    `json:"scopeType"`
	ScopeLabel      string    `json:"scopeLabel"`
	Role            string    `json:"role"`
	ExpiresAt       time.Time `json:"expiresAt"`
	RequiresAccount bool      `json:"requiresAccount"` // true ⇒ no user for Email yet → collect displayName+password
	SignedInMatches bool      `json:"signedInMatches"` // true ⇒ caller is signed in AS Email → one-click attach
}

// AcceptInvitationRequest is POST /api/auth/invitations/{token}/accept. For a
// NEW account (RequiresAccount) displayName+password are required (runtime-
// validated); for an existing/attached account both are ignored. They are
// therefore optional on the wire (omitempty ⇒ tygo emits `displayName?`,
// `password?`), matching how the frontend actually sends them.
type AcceptInvitationRequest struct {
	DisplayName string `json:"displayName,omitempty"`
	Password    string `json:"password,omitempty"`
}
