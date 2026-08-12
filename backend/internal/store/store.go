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
// through it; WithTx composes multi-table writes atomically. It is deliberately
// split into per-aggregate sub-interfaces (embedded below) so consumers and
// mocks can depend on the narrow slice they need rather than a god-interface.
type Store interface {
	// Ping verifies the pool can reach Postgres.
	Ping(ctx context.Context) error
	// RunMigrations applies all pending migrations idempotently and returns
	// the resulting schema version.
	RunMigrations() (version uint, err error)
	// WithTx runs fn inside a transaction, committing on nil and rolling back
	// on error. The Store passed to fn is transaction-scoped.
	WithTx(ctx context.Context, fn func(Store) error) error
	// AdvisoryLock takes a transaction-scoped Postgres advisory lock (released
	// automatically at commit/rollback). It MUST be called inside WithTx; it
	// serializes critical sections such as the first-run bootstrap and (later)
	// the quota reservation, safely across multiple backend instances.
	AdvisoryLock(ctx context.Context, key int64) error

	// GetTenantBySlug returns the tenant with the given slug, or ErrNotFound.
	GetTenantBySlug(ctx context.Context, slug string) (*Tenant, error)
	// ListTenants returns all tenants ordered by slug.
	ListTenants(ctx context.Context) ([]Tenant, error)
	// GetProjectByPoolID returns the project whose Proxmox pool matches
	// poolID, or ErrNotFound.
	GetProjectByPoolID(ctx context.Context, poolID string) (*Project, error)
	// ListProjectsByTenant returns a tenant's projects ordered by slug.
	ListProjectsByTenant(ctx context.Context, tenantID string) ([]Project, error)

	UserStore
	SessionStore
	MembershipStore
}

// UserStore is the users aggregate.
type UserStore interface {
	// CreateUser inserts a new global identity and returns the stored row.
	CreateUser(ctx context.Context, p CreateUserParams) (*User, error)
	// GetUserByEmail looks a user up case-insensitively, or ErrNotFound.
	GetUserByEmail(ctx context.Context, email string) (*User, error)
	// GetUserByID returns the user with the given id, or ErrNotFound.
	GetUserByID(ctx context.Context, id string) (*User, error)
	// CountUsers returns the total number of user rows (drives first-run bootstrap).
	CountUsers(ctx context.Context) (int, error)
	// UpdatePasswordHash replaces a user's password hash and algo (rehash path).
	UpdatePasswordHash(ctx context.Context, userID, passwordHash, passwordAlgo string) error
	// SetTOTPEnabled flips the totp_enabled flag (Phase 5 wiring; stubbed use now).
	SetTOTPEnabled(ctx context.Context, userID string, enabled bool) error
}

// SessionStore is the server-side sessions aggregate.
type SessionStore interface {
	// CreateSession inserts a session row and returns it.
	CreateSession(ctx context.Context, p CreateSessionParams) (*Session, error)
	// GetSessionByTokenHash returns the session for a token hash, or ErrNotFound.
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error)
	// TouchSession bumps last_seen_at (throttled by the caller).
	TouchSession(ctx context.Context, sessionID string, at time.Time) error
	// RevokeSession sets revoked_at on a single session (logout).
	RevokeSession(ctx context.Context, sessionID string) error
	// RevokeOtherUserSessions revokes every one of a user's sessions except
	// keepSessionID (privilege-change / password-change fixation defense).
	RevokeOtherUserSessions(ctx context.Context, userID, keepSessionID string) error
	// ListSessionsByUser returns a user's non-revoked, non-absolute-expired
	// sessions, newest activity first. The idle-timeout window is applied by the
	// caller (auth.Sessions.Live), since idleTTL is an app-layer setting.
	ListSessionsByUser(ctx context.Context, userID string) ([]Session, error)
}

// MembershipStore is the memberships aggregate.
type MembershipStore interface {
	// CreateMembership grants a role at a scope and returns the stored row.
	CreateMembership(ctx context.Context, p CreateMembershipParams) (*Membership, error)
	// ListMembershipsByUser returns every membership held by a user.
	ListMembershipsByUser(ctx context.Context, userID string) ([]Membership, error)
}

// CreateUserParams are the inputs to CreateUser; generated columns (id,
// timestamps, flags defaulting false) are filled by the database.
type CreateUserParams struct {
	Email           string
	DisplayName     string
	PasswordHash    string
	PasswordAlgo    string // "bcrypt" | "argon2id"
	IsPlatformAdmin bool
}

// CreateSessionParams are the inputs to CreateSession.
type CreateSessionParams struct {
	UserID            string
	TokenHash         string
	AbsoluteExpiresAt time.Time
	IP                *string
	UserAgent         *string
}

// CreateMembershipParams are the inputs to CreateMembership.
type CreateMembershipParams struct {
	UserID    string
	ScopeType string // "tenant" | "project"
	ScopeID   string
	Role      string // "owner" | "contributor" | "reader"
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
