package types

import "time"

// Tenant ≙ Azure Directory — the top of the scoping hierarchy.
type Tenant struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TenantMembership is one entry in Me.tenants: a tenant the caller can reach
// plus their highest role anywhere in it (display only; enforcement is per-scope).
type TenantMembership struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	Role string `json:"role"` // owner | contributor | reader | ""
}

// Project ≙ Resource Group, backed by the Proxmox pool PoolID.
type Project struct {
	ID        string    `json:"id"`
	TenantID  string    `json:"tenantId"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	PoolID    string    `json:"poolId"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Member is one tenant member (read-only in Phase 3; invites land in Phase 5).
type Member struct {
	UserID      string `json:"userId"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName"`
	ScopeType   string `json:"scopeType"` // tenant | project
	ScopeID     string `json:"scopeId"`
	Role        string `json:"role"`
}

// TenantSummary is the tenant dashboard header (GET …/summary).
type TenantSummary struct {
	Tenant        Tenant `json:"tenant"`
	Role          string `json:"role"`
	ProjectCount  int    `json:"projectCount"`
	ResourceCount int    `json:"resourceCount"`
}

// CreateProjectRequest is POST …/projects.
type CreateProjectRequest struct {
	Name string `json:"name"`
}

// RenameProjectRequest is PATCH …/projects/{projectId}.
type RenameProjectRequest struct {
	Name string `json:"name"`
}

// DeleteProjectRequest is DELETE …/projects/{projectId}.
type DeleteProjectRequest struct {
	ConfirmName string `json:"confirmName"`
}

// CreateTenantRequest is POST /api/admin/tenants.
type CreateTenantRequest struct {
	Name string `json:"name"`
}

// SetActiveTenantRequest is PATCH /api/auth/active-tenant.
type SetActiveTenantRequest struct {
	TenantId string `json:"tenantId"`
}

// CatalogNode is a placeable node in the wizard catalog (names only — no
// capacity detail for non-admins, ADR-0007 §4).
type CatalogNode struct {
	Name string `json:"name"`
}

// CatalogStorage is a storage in the wizard catalog: id + content types only.
// Capacity (free/total) is deliberately omitted for non-admins — it is never
// emitted as a fabricated zero (ADR-0007 §4, iron rule "never invent data").
type CatalogStorage struct {
	Storage string   `json:"storage"`
	Type    string   `json:"type"`
	Content []string `json:"content"`
}
