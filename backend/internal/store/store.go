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
	TenantStore
	ProjectStore
	OwnershipStore
	QuotaStore
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
	// ListUsersByIDs resolves a batch of user ids to their rows, keyed by id.
	// Missing ids are simply absent from the map (no error). Used to enrich
	// ownership rows with the creator's display name without an N+1 fan-out.
	ListUsersByIDs(ctx context.Context, ids []string) (map[string]User, error)
}

// TenantStore is the tenants aggregate.
type TenantStore interface {
	// CreateTenant inserts a tenant (name + pre-derived slug) and returns it.
	CreateTenant(ctx context.Context, p CreateTenantParams) (*Tenant, error)
	// GetTenantByID returns the tenant with the given id, or ErrNotFound.
	GetTenantByID(ctx context.Context, id string) (*Tenant, error)
	// ListTenantsForUser returns every tenant the user can reach — via a direct
	// tenant-scope membership or via a project-scope membership inside it — each
	// tagged with the user's highest role anywhere in that tenant (display only).
	ListTenantsForUser(ctx context.Context, userID string) ([]TenantWithRole, error)
}

// ProjectStore is the projects aggregate.
type ProjectStore interface {
	// CreateProject inserts a project (tenant, name, slug, pool id) and returns it.
	CreateProject(ctx context.Context, p CreateProjectParams) (*Project, error)
	// GetProjectByID returns the project with the given id, or ErrNotFound.
	GetProjectByID(ctx context.Context, id string) (*Project, error)
	// RenameProject changes only the display name (poolId/slug never change,
	// ADR-0008) and returns the updated row, or ErrNotFound.
	RenameProject(ctx context.Context, id, name string) (*Project, error)
	// DeleteProject removes the project row. Callers MUST verify emptiness first
	// (CountActiveOwnershipByProject == 0); the DB does not cascade-delete guests.
	DeleteProject(ctx context.Context, id string) error
	// CountActiveOwnershipByProject counts the project's live ownership rows
	// (status active or pending — everything not tombstoned), i.e. the guests a
	// delete would orphan. It is the emptiness gate for project deletion.
	CountActiveOwnershipByProject(ctx context.Context, projectID string) (int, error)
}

// OwnershipStore is the resource_ownership aggregate: the VMID -> project ->
// tenant map that the IDOR check and quota accounting stand on.
type OwnershipStore interface {
	// GetOwnershipByVMID returns the ownership row for a VMID, or ErrNotFound.
	GetOwnershipByVMID(ctx context.Context, vmid int) (*ResourceOwnership, error)
	// CreateOwnership inserts an ownership row. Status is "pending" (a create
	// reservation, finalized on task success) or "active" (backfill of an
	// already-existing guest). The Phase-4 quota reservation will wrap this in
	// WithTx+AdvisoryLock; Phase 3 inserts directly.
	CreateOwnership(ctx context.Context, p CreateOwnershipParams) (*ResourceOwnership, error)
	// FinalizeOwnership transitions a pending row to active and records the
	// creating task's UPID. ErrNotFound if the row is missing or not pending.
	FinalizeOwnership(ctx context.Context, id, upid string) error
	// ReleaseOwnership deletes a still-pending reservation (a create that failed
	// or timed out), freeing the VMID for reuse. ErrNotFound if not pending.
	ReleaseOwnership(ctx context.Context, id string) error
	// TombstoneOwnership marks a row tombstoned (the Phase-4 reconciler's verdict
	// for a guest that vanished from Proxmox). ErrNotFound if the row is missing.
	TombstoneOwnership(ctx context.Context, id string) error
	// ListOwnershipByTenant returns the tenant's ownership rows (tenant filter in
	// SQL), ordered by VMID.
	ListOwnershipByTenant(ctx context.Context, tenantID string) ([]ResourceOwnership, error)
	// ListOwnershipByProject returns the project's ownership rows, ordered by VMID.
	ListOwnershipByProject(ctx context.Context, projectID string) ([]ResourceOwnership, error)
	// ListActiveVMIDs returns the set of VMIDs with a live ownership row (active
	// or pending; tombstoned excluded). Drives the backfill/reconciler's
	// "already owned?" check in one query.
	ListActiveVMIDs(ctx context.Context) (map[int]bool, error)
	// ListStalePendingOwnership returns pending reservation rows created strictly
	// before olderThan — the Phase-4 reconciler's stale-pending sweep set (a
	// backend that died mid-create leaves a pending row that leaks quota forever
	// without this). Parameterized on the cutoff; ordered by created_at.
	ListStalePendingOwnership(ctx context.Context, olderThan time.Time) ([]ResourceOwnership, error)
}

// QuotaStore is the quotas + reservation + audit aggregate (ADR-0009/0010/0012).
// Usage is a Go aggregation over one tenant-filtered ownership SELECT joined to
// a caller-supplied ClusterResources snapshot — there is no drift-prone counter.
type QuotaStore interface {
	// GetQuota returns the stored limits for a scope ("tenant"|"project"), or
	// ErrNotFound — which the caller treats as all-unlimited (no cap on any
	// dimension), never as an error.
	GetQuota(ctx context.Context, scopeType, scopeID string) (*Quota, error)
	// UpsertQuota inserts or replaces a scope's limits (INSERT … ON CONFLICT
	// (scope_type, scope_id) DO UPDATE) and returns the stored row. A nil limit
	// field clears that dimension (→ unlimited).
	UpsertQuota(ctx context.Context, p UpsertQuotaParams) (*Quota, error)
	// ComputeUsage aggregates a tenant's live usage from one tenant-filtered
	// SELECT of active+pending ownership rows joined to snapshot: an active row
	// reads snapshot[vmid] (absent ⇒ 0, no count); a pending row reads its
	// reserved_* columns (always counted). Returns the tenant total plus a
	// per-project breakdown (ADR-0012 §1.3).
	ComputeUsage(ctx context.Context, tenantID string, snapshot map[int]Alloc) (tenant QuotaUsage, byProject map[string]QuotaUsage, err error)
	// ReserveOwnership is the concurrency-safe create reservation (ADR-0012 §2).
	// It runs its own WithTx + AdvisoryLock(AdvisoryKeyTenant(tenantID)), re-reads
	// active+pending usage under the lock, checks each non-null dimension of the
	// project AND tenant quota (usage+delta ≤ limit) — first violation →
	// ErrQuotaExceeded — then inserts the pending row with reserved_* set
	// (duplicate VMID → ErrConflict). Commit releases the lock.
	ReserveOwnership(ctx context.Context, p ReserveOwnershipParams) (*ResourceOwnership, error)
	// InsertAuditIntent writes a fail-closed intent row (outcome "pending") at the
	// audit choke-point and returns its id (ADR-0012 §3). It is one of the only
	// two permitted audit mutations.
	InsertAuditIntent(ctx context.Context, a AuditIntent) (id string, err error)
	// FinalizeAudit performs the one-way outcome/detail finalize on the middleware's
	// own intent row — the only other permitted audit mutation. No general
	// UPDATE/DELETE is exposed, so who/what/when stays immutable.
	FinalizeAudit(ctx context.Context, id, outcome string, detail []byte) error
	// ListAudit returns a tenant's audit rows, keyset-paginated by (ts, id) DESC.
	ListAudit(ctx context.Context, q AuditQuery) ([]AuditEntry, error)
}

// SessionStore is the server-side sessions aggregate.
type SessionStore interface {
	// CreateSession inserts a session row and returns it.
	CreateSession(ctx context.Context, p CreateSessionParams) (*Session, error)
	// GetSessionByTokenHash returns the session for a token hash, or ErrNotFound.
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error)
	// TouchSession bumps last_seen_at (throttled by the caller).
	TouchSession(ctx context.Context, sessionID string, at time.Time) error
	// SetSessionActiveTenant sets (or clears, when tenantID is nil) the session's
	// active_tenant_id — the tenant switch behind PATCH /api/auth/active-tenant.
	// ErrNotFound if the session id does not exist.
	SetSessionActiveTenant(ctx context.Context, sessionID string, tenantID *string) error
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
	// ListMembershipsByScope returns every membership at one scope
	// (scopeType "tenant"|"project", scopeID the tenant/project id) — the
	// tenant members list, filtered in SQL.
	ListMembershipsByScope(ctx context.Context, scopeType, scopeID string) ([]Membership, error)
	// ListMembershipsByScopes returns every membership at any of scopeIDs of the
	// given scopeType, in one query (scope_id = ANY($2)). It replaces a per-scope
	// fan-out (the members list resolving each project's grants); an empty
	// scopeIDs slice is a cheap empty result, not a query.
	ListMembershipsByScopes(ctx context.Context, scopeType string, scopeIDs []string) ([]Membership, error)
	// GetEffectiveRoles resolves, in a single tenant-filtered membership scan,
	// the user's highest tenant-scope role in tenantID ("" if none) and a
	// project-id -> highest-role map for every project of that tenant the user
	// holds a project-scope membership in. Backs ResolveTenant/ResolveScope.
	GetEffectiveRoles(ctx context.Context, userID, tenantID string) (tenantRole string, projectRoles map[string]string, err error)
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

// CreateTenantParams are the inputs to CreateTenant. Slug is caller-derived
// (collision-suffixed) so the store never guesses a display-name transform.
type CreateTenantParams struct {
	Name string
	Slug string
}

// CreateProjectParams are the inputs to CreateProject. PoolID is the Proxmox
// pool the project mirrors (pc-<tenant>-<project>); slug is caller-derived.
type CreateProjectParams struct {
	TenantID string
	Name     string
	Slug     string
	PoolID   string
}

// CreateOwnershipParams are the inputs to CreateOwnership. Status is "pending"
// (a create reservation) or "active" (backfill of an existing guest); CreatedBy
// is nil for backfilled/system-claimed rows. The Reserved* fields are the
// provisioned allocation of a pending reservation (ADR-0012 §2); nil for
// backfilled/active rows whose usage reads the live snapshot instead.
type CreateOwnershipParams struct {
	TenantID       string
	ProjectID      string
	VMID           int
	GuestType      string // "qemu" | "lxc"
	Node           string
	CreatedBy      *string
	Status         string // "pending" | "active"
	ReservedVCPU   *int
	ReservedRAMMB  *int64
	ReservedDiskGB *int64
}

// Alloc is a single guest's resource allocation, in quota units and PVE-free:
// the handler fills it from a ClusterResources snapshot (MaxCPU cores, MaxMem→MB,
// MaxDisk→GB provisioned) so the store aggregates without importing proxmox.
type Alloc struct {
	VCPU   int
	RAMMB  int64
	DiskGB int64
}

// QuotaUsage is aggregated live usage for a scope. Sums PROVISIONED allocations
// (ADR-0012 §4): active guests from the snapshot, pending reservations from
// reserved_*.
type QuotaUsage struct {
	VCPU   int
	RAMMB  int64
	DiskGB int64
	Count  int
}

// UpsertQuotaParams are the inputs to UpsertQuota. A nil limit clears that
// dimension (→ unlimited).
type UpsertQuotaParams struct {
	ScopeType string // "tenant" | "project"
	ScopeID   string
	MaxVCPU   *int
	MaxRAMMB  *int64
	MaxDiskGB *int64
	MaxCount  *int
}

// ReserveOwnershipParams are the inputs to ReserveOwnership. Reserved is the
// requested delta for this create; Snapshot is the active-guest allocation map
// fetched from ClusterResources BEFORE the lock (so no PVE round-trip is ever
// held under the advisory lock — ADR-0009).
type ReserveOwnershipParams struct {
	TenantID  string
	ProjectID string
	VMID      int
	GuestType string // "qemu" | "lxc"
	Node      string
	CreatedBy *string
	Reserved  Alloc
	Snapshot  map[int]Alloc
}

// AuditIntent is the fail-closed intent row written before a mutation runs
// (outcome defaults to "pending"). ProjectID is nil at tenant level; TargetType/
// TargetID are nil for creates whose id is not yet known.
type AuditIntent struct {
	ActorUserID *string
	TenantID    *string
	ProjectID   *string
	Action      string
	TargetType  *string
	TargetID    *string
	IP          *string
}

// AuditQuery filters the audit spine. TenantID is required (tenant filter in SQL);
// Before is the keyset cursor (rows strictly older than it); ProjectID/Outcome are
// optional narrowing filters; Limit is clamped by the caller.
type AuditQuery struct {
	TenantID  string
	Before    *time.Time
	Limit     int
	ProjectID string // "" = no project filter
	Outcome   string // "" = no outcome filter
}

// TenantWithRole is a tenant plus the requesting user's highest role anywhere
// in it (display only; enforcement is always per-scope). Returned by
// ListTenantsForUser to build the Me.tenants switcher.
type TenantWithRole struct {
	Tenant
	Role string // "owner" | "contributor" | "reader" | ""
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
	// Provisioned allocation of a pending reservation (ADR-0012 §2). Nil for
	// active/backfilled rows, whose usage is read from the live snapshot.
	ReservedVCPU   *int
	ReservedRAMMB  *int64
	ReservedDiskGB *int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
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
