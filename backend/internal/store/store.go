// Package store is the repository layer over the Postgres system of record
// (ADR-0005). All persistence sits behind the Store interface so handlers,
// authz, quotas, and audit are unit-testable against a mock — mirroring the
// proxmox.Client seam. The pgx implementation lives in postgres.go.
//
// Phase 1 scope: only the schema, migrations, connectivity, and the tenant/
// project getters needed for a boot smoke test are implemented. Later phases
// extend this interface (per-aggregate sub-interfaces for users, sessions,
// memberships, ownership, quotas, audit) — the domain structs below already
// mirror the full schema so those additions are additive.
package store

import (
	"context"
	"time"
)

// Store is the repository interface. Everything that touches Postgres goes
// through it; WithTx composes multi-table writes atomically.
type Store interface {
	// Ping verifies the pool can reach Postgres.
	Ping(ctx context.Context) error
	// RunMigrations applies all pending migrations idempotently and returns
	// the resulting schema version.
	RunMigrations() (version uint, err error)
	// WithTx runs fn inside a transaction, committing on nil and rolling back
	// on error. The Store passed to fn is transaction-scoped.
	WithTx(ctx context.Context, fn func(Store) error) error

	// GetTenantBySlug returns the tenant with the given slug, or ErrNotFound.
	GetTenantBySlug(ctx context.Context, slug string) (*Tenant, error)
	// ListTenants returns all tenants ordered by slug.
	ListTenants(ctx context.Context) ([]Tenant, error)
	// GetProjectByPoolID returns the project whose Proxmox pool matches
	// poolID, or ErrNotFound.
	GetProjectByPoolID(ctx context.Context, poolID string) (*Project, error)
	// ListProjectsByTenant returns a tenant's projects ordered by slug.
	ListProjectsByTenant(ctx context.Context, tenantID string) ([]Project, error)
}

// User is a global identity. Access to tenants/projects flows via Membership.
type User struct {
	ID              string
	Email           string
	DisplayName     string
	PasswordHash    *string // nil until a real user is created (Phase 2)
	PasswordAlgo    *string // "bcrypt" | "argon2id"
	IsPlatformAdmin bool
	TOTPEnabled     bool
	Disabled        bool
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Tenant ≙ Azure Directory.
type Tenant struct {
	ID        string
	Name      string
	Slug      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Project ≙ Resource Group, backed by Proxmox pool PoolID.
type Project struct {
	ID        string
	TenantID  string
	Name      string
	Slug      string
	PoolID    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Membership grants Role at a tenant or project scope.
type Membership struct {
	ID        string
	UserID    string
	ScopeType string // "tenant" | "project"
	ScopeID   string
	Role      string // "owner" | "contributor" | "reader"
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ResourceOwnership maps a VMID to its owning project/tenant.
type ResourceOwnership struct {
	ID        string
	TenantID  string
	ProjectID string
	VMID      int
	GuestType string // "qemu" | "lxc"
	Node      string
	CreatedBy *string
	Status    string // "pending" | "active" | "tombstoned"
	PVEUPID   *string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Quota holds per-scope limits; a nil field means unlimited.
type Quota struct {
	ID        string
	ScopeType string // "tenant" | "project"
	ScopeID   string
	MaxVCPU   *int
	MaxRAMMB  *int64
	MaxDiskGB *int64
	MaxCount  *int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Session is a server-side session; the opaque token is stored only as
// TokenHash.
type Session struct {
	ID                string
	TokenHash         string
	UserID            string
	ActiveTenantID    *string
	CreatedAt         time.Time
	LastSeenAt        time.Time
	AbsoluteExpiresAt time.Time
	RevokedAt         *time.Time
	IP                *string
	UserAgent         *string
}

// Invitation is a single-use membership invite; the token is stored hashed and
// the role is bound in the row (tamper-proof).
type Invitation struct {
	ID         string
	TokenHash  string
	Email      string
	ScopeType  string
	ScopeID    string
	Role       string
	InvitedBy  *string
	ExpiresAt  time.Time
	AcceptedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// AuditEntry is one append-only audit record.
type AuditEntry struct {
	ID          string
	TS          time.Time
	ActorUserID *string
	TenantID    *string
	ProjectID   *string
	Action      string
	TargetType  *string
	TargetID    *string
	Outcome     string
	IP          *string
	Detail      []byte // jsonb
}
