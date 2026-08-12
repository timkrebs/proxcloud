package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx" for golang-migrate

	"github.com/timkrebs9/proxcloud/backend/migrations"
)

// ErrNotFound is returned by getters when no row matches.
var ErrNotFound = errors.New("store: not found")

// ErrConflict is returned by writers when a row violates a uniqueness
// constraint — a duplicate tenant/project slug, a duplicate pool id, or a VMID
// that is already reserved. Handlers map it to HTTP 409 rather than leaking the
// raw Postgres error. It is the write-path counterpart to ErrNotFound.
var ErrConflict = errors.New("store: conflict")

// isUniqueViolation reports whether err (or anything it wraps) is a Postgres
// unique-violation, SQLSTATE 23505 — the signal that a slug/pool/VMID collided
// with an existing UNIQUE constraint. Used to translate the raw pgx error into
// the ErrConflict sentinel at the Create* boundary.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// queryer is the subset of pgx shared by *pgxpool.Pool and pgx.Tx, so the same
// query methods serve both pooled and transaction-scoped stores.
type queryer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// PgStore is the Postgres-backed Store. The zero value is not usable; construct
// with New.
type PgStore struct {
	q    queryer       // pool, or a tx inside WithTx
	pool *pgxpool.Pool // nil for a tx-scoped store
	dsn  string        // retained for golang-migrate's database/sql handle
}

// compile-time assurance the pgx impl satisfies the interface.
var _ Store = (*PgStore)(nil)

// New opens a pooled connection to dsn and verifies reachability with a ping.
// The caller owns Close.
func New(ctx context.Context, dsn string) (*PgStore, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &PgStore{q: pool, pool: pool, dsn: dsn}, nil
}

// Close releases the connection pool. Safe on a nil/tx-scoped store.
func (s *PgStore) Close() {
	if s.pool != nil {
		s.pool.Close()
	}
}

// Ping verifies the pool can reach Postgres.
func (s *PgStore) Ping(ctx context.Context) error {
	if s.pool == nil {
		return errors.New("store: Ping requires a pooled store")
	}
	return s.pool.Ping(ctx)
}

// RunMigrations applies all pending migrations idempotently, using the embedded
// SQL files as the source and a database/sql handle (pgx stdlib driver) as the
// database. Returns the resulting schema version.
//
// It takes no context: golang-migrate v4's Migrate.Up() is not
// context-aware, so a migration that blocks (e.g. on another instance's lock)
// has no cancellation path. Acceptable at single-instance startup; revisit if
// multi-replica deploys ever contend on migrations.
func (s *PgStore) RunMigrations() (uint, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return 0, fmt.Errorf("store: migration source: %w", err)
	}
	defer func() { _ = src.Close() }()

	db, err := sql.Open("pgx", s.dsn)
	if err != nil {
		return 0, fmt.Errorf("store: open migrate db: %w", err)
	}
	defer func() { _ = db.Close() }()

	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		return 0, fmt.Errorf("store: migrate driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		return 0, fmt.Errorf("store: migrate init: %w", err)
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return 0, fmt.Errorf("store: migrate up: %w", err)
	}
	version, dirty, err := m.Version()
	if err != nil {
		return 0, fmt.Errorf("store: migrate version: %w", err)
	}
	if dirty {
		return version, fmt.Errorf("store: migration state is dirty at version %d", version)
	}
	return version, nil
}

// WithTx runs fn inside a single transaction. It is reentrant: when called on
// an already transaction-scoped store (s.pool == nil), fn JOINS the existing
// transaction rather than opening a second, independent one. This preserves
// atomicity for composed critical sections — e.g. the quota reservation
// (ADR-0009) and the audit write (ADR-0010) that must commit-or-rollback
// together — where a naive nested Begin would silently lose atomicity and risk
// self-deadlock on a small pool.
func (s *PgStore) WithTx(ctx context.Context, fn func(Store) error) error {
	if s.pool == nil {
		// Already in a transaction: reuse it so nested work is atomic with the
		// outer tx. An error propagates up and the outermost WithTx rolls back
		// the whole transaction.
		return fn(s)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	txStore := &PgStore{q: tx, dsn: s.dsn} // pool nil → tx-scoped, drives reuse above
	if err := fn(txStore); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("store: commit tx: %w", err)
	}
	return nil
}

// AdvisoryLock takes a transaction-scoped advisory lock. It must run inside a
// transaction (s.pool == nil): pg_advisory_xact_lock is released only at
// commit/rollback, so calling it on a pooled connection would leak the lock.
func (s *PgStore) AdvisoryLock(ctx context.Context, key int64) error {
	if s.pool != nil {
		return errors.New("store: AdvisoryLock must be called inside WithTx")
	}
	if _, err := s.q.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, key); err != nil {
		return fmt.Errorf("store: advisory lock: %w", err)
	}
	return nil
}

const userColumns = `id::text, email, display_name, password_hash, password_algo,
	is_platform_admin, totp_enabled, disabled, created_at, updated_at`

func scanUser(row pgx.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.PasswordHash, &u.PasswordAlgo,
		&u.IsPlatformAdmin, &u.TOTPEnabled, &u.Disabled, &u.CreatedAt, &u.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// CreateUser inserts a global identity and returns the stored row.
func (s *PgStore) CreateUser(ctx context.Context, p CreateUserParams) (*User, error) {
	const q = `INSERT INTO users (email, display_name, password_hash, password_algo, is_platform_admin)
	           VALUES ($1, $2, $3, $4, $5)
	           RETURNING ` + userColumns
	u, err := scanUser(s.q.QueryRow(ctx, q, p.Email, p.DisplayName, p.PasswordHash, p.PasswordAlgo, p.IsPlatformAdmin))
	if err != nil {
		return nil, fmt.Errorf("store: create user: %w", err)
	}
	return u, nil
}

// GetUserByEmail looks a user up case-insensitively.
func (s *PgStore) GetUserByEmail(ctx context.Context, email string) (*User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE lower(email) = lower($1)`
	u, err := scanUser(s.q.QueryRow(ctx, q, email))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get user by email: %w", err)
	}
	return u, nil
}

// GetUserByID returns the user with the given id.
func (s *PgStore) GetUserByID(ctx context.Context, id string) (*User, error) {
	const q = `SELECT ` + userColumns + ` FROM users WHERE id = $1::uuid`
	u, err := scanUser(s.q.QueryRow(ctx, q, id))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get user by id: %w", err)
	}
	return u, nil
}

// CountUsers returns the total number of user rows.
func (s *PgStore) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.q.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count users: %w", err)
	}
	return n, nil
}

// UpdatePasswordHash replaces a user's password hash and algo.
func (s *PgStore) UpdatePasswordHash(ctx context.Context, userID, passwordHash, passwordAlgo string) error {
	const q = `UPDATE users SET password_hash = $2, password_algo = $3, updated_at = now()
	           WHERE id = $1::uuid`
	tag, err := s.q.Exec(ctx, q, userID, passwordHash, passwordAlgo)
	if err != nil {
		return fmt.Errorf("store: update password hash: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTOTPEnabled flips the totp_enabled flag.
func (s *PgStore) SetTOTPEnabled(ctx context.Context, userID string, enabled bool) error {
	const q = `UPDATE users SET totp_enabled = $2, updated_at = now() WHERE id = $1::uuid`
	tag, err := s.q.Exec(ctx, q, userID, enabled)
	if err != nil {
		return fmt.Errorf("store: set totp enabled: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const sessionColumns = `id::text, token_hash, user_id::text, active_tenant_id::text,
	created_at, last_seen_at, absolute_expires_at, revoked_at, ip, user_agent`

func scanSession(row pgx.Row) (*Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.TokenHash, &s.UserID, &s.ActiveTenantID,
		&s.CreatedAt, &s.LastSeenAt, &s.AbsoluteExpiresAt, &s.RevokedAt, &s.IP, &s.UserAgent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// CreateSession inserts a session row and returns it.
func (s *PgStore) CreateSession(ctx context.Context, p CreateSessionParams) (*Session, error) {
	const q = `INSERT INTO sessions (token_hash, user_id, absolute_expires_at, ip, user_agent)
	           VALUES ($1, $2::uuid, $3, $4, $5)
	           RETURNING ` + sessionColumns
	sess, err := scanSession(s.q.QueryRow(ctx, q, p.TokenHash, p.UserID, p.AbsoluteExpiresAt, p.IP, p.UserAgent))
	if err != nil {
		return nil, fmt.Errorf("store: create session: %w", err)
	}
	return sess, nil
}

// GetSessionByTokenHash returns the session for a token hash.
func (s *PgStore) GetSessionByTokenHash(ctx context.Context, tokenHash string) (*Session, error) {
	const q = `SELECT ` + sessionColumns + ` FROM sessions WHERE token_hash = $1`
	sess, err := scanSession(s.q.QueryRow(ctx, q, tokenHash))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get session by token hash: %w", err)
	}
	return sess, nil
}

// TouchSession bumps last_seen_at.
func (s *PgStore) TouchSession(ctx context.Context, sessionID string, at time.Time) error {
	const q = `UPDATE sessions SET last_seen_at = $2 WHERE id = $1::uuid`
	if _, err := s.q.Exec(ctx, q, sessionID, at); err != nil {
		return fmt.Errorf("store: touch session: %w", err)
	}
	return nil
}

// SetSessionActiveTenant sets or clears the session's active_tenant_id.
func (s *PgStore) SetSessionActiveTenant(ctx context.Context, sessionID string, tenantID *string) error {
	const q = `UPDATE sessions SET active_tenant_id = $2::uuid WHERE id = $1::uuid`
	tag, err := s.q.Exec(ctx, q, sessionID, tenantID)
	if err != nil {
		return fmt.Errorf("store: set session active tenant: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// RevokeSession sets revoked_at on a single session.
func (s *PgStore) RevokeSession(ctx context.Context, sessionID string) error {
	const q = `UPDATE sessions SET revoked_at = now() WHERE id = $1::uuid AND revoked_at IS NULL`
	if _, err := s.q.Exec(ctx, q, sessionID); err != nil {
		return fmt.Errorf("store: revoke session: %w", err)
	}
	return nil
}

// RevokeOtherUserSessions revokes every one of a user's sessions except keepSessionID.
func (s *PgStore) RevokeOtherUserSessions(ctx context.Context, userID, keepSessionID string) error {
	const q = `UPDATE sessions SET revoked_at = now()
	           WHERE user_id = $1::uuid AND id <> $2::uuid AND revoked_at IS NULL`
	if _, err := s.q.Exec(ctx, q, userID, keepSessionID); err != nil {
		return fmt.Errorf("store: revoke other sessions: %w", err)
	}
	return nil
}

// ListSessionsByUser returns a user's live sessions, newest activity first.
func (s *PgStore) ListSessionsByUser(ctx context.Context, userID string) ([]Session, error) {
	const q = `SELECT ` + sessionColumns + ` FROM sessions
	           WHERE user_id = $1::uuid AND revoked_at IS NULL AND absolute_expires_at > now()
	           ORDER BY last_seen_at DESC`
	rows, err := s.q.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	defer rows.Close()

	out := []Session{}
	for rows.Next() {
		sess, err := scanSession(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan session: %w", err)
		}
		out = append(out, *sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list sessions: %w", err)
	}
	return out, nil
}

// CreateMembership grants a role at a scope and returns the stored row.
func (s *PgStore) CreateMembership(ctx context.Context, p CreateMembershipParams) (*Membership, error) {
	const q = `INSERT INTO memberships (user_id, scope_type, scope_id, role)
	           VALUES ($1::uuid, $2, $3::uuid, $4)
	           RETURNING id::text, user_id::text, scope_type, scope_id::text, role, created_at, updated_at`
	var m Membership
	err := s.q.QueryRow(ctx, q, p.UserID, p.ScopeType, p.ScopeID, p.Role).Scan(
		&m.ID, &m.UserID, &m.ScopeType, &m.ScopeID, &m.Role, &m.CreatedAt, &m.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("store: create membership: %w", err)
	}
	return &m, nil
}

// ListMembershipsByUser returns every membership held by a user.
func (s *PgStore) ListMembershipsByUser(ctx context.Context, userID string) ([]Membership, error) {
	const q = `SELECT id::text, user_id::text, scope_type, scope_id::text, role, created_at, updated_at
	           FROM memberships WHERE user_id = $1::uuid ORDER BY created_at`
	rows, err := s.q.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list memberships: %w", err)
	}
	defer rows.Close()

	out := []Membership{}
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.ID, &m.UserID, &m.ScopeType, &m.ScopeID, &m.Role, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan membership: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list memberships: %w", err)
	}
	return out, nil
}

// GetTenantBySlug returns the tenant with the given slug.
func (s *PgStore) GetTenantBySlug(ctx context.Context, slug string) (*Tenant, error) {
	const q = `SELECT id::text, name, slug, created_at, updated_at
	           FROM tenants WHERE slug = $1`
	var t Tenant
	err := s.q.QueryRow(ctx, q, slug).Scan(&t.ID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get tenant by slug: %w", err)
	}
	return &t, nil
}

// ListTenants returns all tenants ordered by slug.
func (s *PgStore) ListTenants(ctx context.Context) ([]Tenant, error) {
	const q = `SELECT id::text, name, slug, created_at, updated_at
	           FROM tenants ORDER BY slug`
	rows, err := s.q.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list tenants: %w", err)
	}
	defer rows.Close()

	out := []Tenant{}
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan tenant: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tenants: %w", err)
	}
	return out, nil
}

// GetProjectByPoolID returns the project mapped to poolID.
func (s *PgStore) GetProjectByPoolID(ctx context.Context, poolID string) (*Project, error) {
	const q = `SELECT id::text, tenant_id::text, name, slug, pool_id, created_at, updated_at
	           FROM projects WHERE pool_id = $1`
	var p Project
	err := s.q.QueryRow(ctx, q, poolID).Scan(
		&p.ID, &p.TenantID, &p.Name, &p.Slug, &p.PoolID, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get project by pool id: %w", err)
	}
	return &p, nil
}

// ListProjectsByTenant returns a tenant's projects ordered by slug.
func (s *PgStore) ListProjectsByTenant(ctx context.Context, tenantID string) ([]Project, error) {
	const q = `SELECT ` + projectColumns + `
	           FROM projects WHERE tenant_id = $1::uuid ORDER BY slug`
	rows, err := s.q.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()

	out := []Project{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan project: %w", err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	return out, nil
}

// --- shared column lists + scanners for the Phase-3 aggregates ---

const tenantColumns = `id::text, name, slug, created_at, updated_at`

func scanTenant(row pgx.Row) (*Tenant, error) {
	var t Tenant
	err := row.Scan(&t.ID, &t.Name, &t.Slug, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

const projectColumns = `id::text, tenant_id::text, name, slug, pool_id, created_at, updated_at`

func scanProject(row pgx.Row) (*Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.TenantID, &p.Name, &p.Slug, &p.PoolID, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// created_by is cast to text so a NULL creator scans cleanly into *string.
const ownershipColumns = `id::text, tenant_id::text, project_id::text, vmid, guest_type,
	node, created_by::text, status, pve_upid, created_at, updated_at`

func scanOwnership(row pgx.Row) (*ResourceOwnership, error) {
	var o ResourceOwnership
	err := row.Scan(&o.ID, &o.TenantID, &o.ProjectID, &o.VMID, &o.GuestType,
		&o.Node, &o.CreatedBy, &o.Status, &o.PVEUPID, &o.CreatedAt, &o.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// roleRank orders the role hierarchy for max-role aggregation in Go
// (owner > contributor > reader > none). The store owns this locally so it
// never has to import authz (authz depends on store, not the reverse).
func roleRank(role string) int {
	switch role {
	case "owner":
		return 3
	case "contributor":
		return 2
	case "reader":
		return 1
	default:
		return 0
	}
}

// ListUsersByIDs implements UserStore.
func (s *PgStore) ListUsersByIDs(ctx context.Context, ids []string) (map[string]User, error) {
	out := map[string]User{}
	if len(ids) == 0 {
		return out, nil
	}
	const q = `SELECT ` + userColumns + ` FROM users WHERE id = ANY($1::uuid[])`
	rows, err := s.q.Query(ctx, q, ids)
	if err != nil {
		return nil, fmt.Errorf("store: list users by ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan user: %w", err)
		}
		out[u.ID] = *u
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list users by ids: %w", err)
	}
	return out, nil
}

// CreateTenant implements TenantStore.
func (s *PgStore) CreateTenant(ctx context.Context, p CreateTenantParams) (*Tenant, error) {
	const q = `INSERT INTO tenants (name, slug) VALUES ($1, $2)
	           RETURNING ` + tenantColumns
	t, err := scanTenant(s.q.QueryRow(ctx, q, p.Name, p.Slug))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("store: create tenant: %w", ErrConflict)
		}
		return nil, fmt.Errorf("store: create tenant: %w", err)
	}
	return t, nil
}

// GetTenantByID implements TenantStore.
func (s *PgStore) GetTenantByID(ctx context.Context, id string) (*Tenant, error) {
	const q = `SELECT ` + tenantColumns + ` FROM tenants WHERE id = $1::uuid`
	t, err := scanTenant(s.q.QueryRow(ctx, q, id))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get tenant by id: %w", err)
	}
	return t, nil
}

// ListTenantsForUser implements TenantStore. A user reaches a tenant either via
// a direct tenant-scope membership or via a project-scope membership inside it;
// the CTE unions both and the outer query keeps the highest role per tenant.
func (s *PgStore) ListTenantsForUser(ctx context.Context, userID string) ([]TenantWithRole, error) {
	const q = `
		WITH utr AS (
			SELECT m.scope_id AS tenant_id, m.role
			FROM memberships m
			WHERE m.user_id = $1::uuid AND m.scope_type = 'tenant'
			UNION ALL
			SELECT p.tenant_id AS tenant_id, m.role
			FROM memberships m
			JOIN projects p ON p.id = m.scope_id
			WHERE m.user_id = $1::uuid AND m.scope_type = 'project'
		)
		SELECT t.id::text, t.name, t.slug, t.created_at, t.updated_at,
			CASE max(CASE utr.role WHEN 'owner' THEN 3 WHEN 'contributor' THEN 2 WHEN 'reader' THEN 1 ELSE 0 END)
				WHEN 3 THEN 'owner' WHEN 2 THEN 'contributor' WHEN 1 THEN 'reader' ELSE '' END AS role
		FROM tenants t
		JOIN utr ON utr.tenant_id = t.id
		GROUP BY t.id, t.name, t.slug, t.created_at, t.updated_at
		ORDER BY t.slug`
	rows, err := s.q.Query(ctx, q, userID)
	if err != nil {
		return nil, fmt.Errorf("store: list tenants for user: %w", err)
	}
	defer rows.Close()

	out := []TenantWithRole{}
	for rows.Next() {
		var tw TenantWithRole
		if err := rows.Scan(&tw.ID, &tw.Name, &tw.Slug, &tw.CreatedAt, &tw.UpdatedAt, &tw.Role); err != nil {
			return nil, fmt.Errorf("store: scan tenant-with-role: %w", err)
		}
		out = append(out, tw)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list tenants for user: %w", err)
	}
	return out, nil
}

// CreateProject implements ProjectStore.
func (s *PgStore) CreateProject(ctx context.Context, p CreateProjectParams) (*Project, error) {
	const q = `INSERT INTO projects (tenant_id, name, slug, pool_id)
	           VALUES ($1::uuid, $2, $3, $4)
	           RETURNING ` + projectColumns
	proj, err := scanProject(s.q.QueryRow(ctx, q, p.TenantID, p.Name, p.Slug, p.PoolID))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("store: create project: %w", ErrConflict)
		}
		return nil, fmt.Errorf("store: create project: %w", err)
	}
	return proj, nil
}

// GetProjectByID implements ProjectStore.
func (s *PgStore) GetProjectByID(ctx context.Context, id string) (*Project, error) {
	const q = `SELECT ` + projectColumns + ` FROM projects WHERE id = $1::uuid`
	p, err := scanProject(s.q.QueryRow(ctx, q, id))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get project by id: %w", err)
	}
	return p, nil
}

// RenameProject implements ProjectStore. Only the display name changes; slug
// and pool_id are immutable (ADR-0008).
func (s *PgStore) RenameProject(ctx context.Context, id, name string) (*Project, error) {
	const q = `UPDATE projects SET name = $2, updated_at = now()
	           WHERE id = $1::uuid
	           RETURNING ` + projectColumns
	p, err := scanProject(s.q.QueryRow(ctx, q, id, name))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: rename project: %w", err)
	}
	return p, nil
}

// DeleteProject implements ProjectStore. The caller checks emptiness first.
func (s *PgStore) DeleteProject(ctx context.Context, id string) error {
	const q = `DELETE FROM projects WHERE id = $1::uuid`
	tag, err := s.q.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("store: delete project: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CountActiveOwnershipByProject implements ProjectStore. Live rows (active or
// pending) block a delete; tombstoned ones do not.
func (s *PgStore) CountActiveOwnershipByProject(ctx context.Context, projectID string) (int, error) {
	const q = `SELECT count(*) FROM resource_ownership
	           WHERE project_id = $1::uuid AND status IN ('active', 'pending')`
	var n int
	if err := s.q.QueryRow(ctx, q, projectID).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: count active ownership by project: %w", err)
	}
	return n, nil
}

// GetOwnershipByVMID implements OwnershipStore.
func (s *PgStore) GetOwnershipByVMID(ctx context.Context, vmid int) (*ResourceOwnership, error) {
	const q = `SELECT ` + ownershipColumns + ` FROM resource_ownership WHERE vmid = $1`
	o, err := scanOwnership(s.q.QueryRow(ctx, q, vmid))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get ownership by vmid: %w", err)
	}
	return o, nil
}

// CreateOwnership implements OwnershipStore. A nil CreatedBy inserts NULL.
func (s *PgStore) CreateOwnership(ctx context.Context, p CreateOwnershipParams) (*ResourceOwnership, error) {
	const q = `INSERT INTO resource_ownership
	             (tenant_id, project_id, vmid, guest_type, node, created_by, status)
	           VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::uuid, $7)
	           RETURNING ` + ownershipColumns
	o, err := scanOwnership(s.q.QueryRow(ctx, q,
		p.TenantID, p.ProjectID, p.VMID, p.GuestType, p.Node, p.CreatedBy, p.Status))
	if err != nil {
		if isUniqueViolation(err) {
			return nil, fmt.Errorf("store: create ownership: %w", ErrConflict)
		}
		return nil, fmt.Errorf("store: create ownership: %w", err)
	}
	return o, nil
}

// FinalizeOwnership implements OwnershipStore: pending -> active + record UPID.
func (s *PgStore) FinalizeOwnership(ctx context.Context, id, upid string) error {
	const q = `UPDATE resource_ownership
	           SET status = 'active', pve_upid = $2, updated_at = now()
	           WHERE id = $1::uuid AND status = 'pending'`
	tag, err := s.q.Exec(ctx, q, id, upid)
	if err != nil {
		return fmt.Errorf("store: finalize ownership: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ReleaseOwnership implements OwnershipStore: delete a still-pending reservation
// so the VMID is free to reuse. Only pending rows are releasable.
func (s *PgStore) ReleaseOwnership(ctx context.Context, id string) error {
	const q = `DELETE FROM resource_ownership WHERE id = $1::uuid AND status = 'pending'`
	tag, err := s.q.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("store: release ownership: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// TombstoneOwnership implements OwnershipStore (Phase-4 reconciler verdict).
func (s *PgStore) TombstoneOwnership(ctx context.Context, id string) error {
	const q = `UPDATE resource_ownership SET status = 'tombstoned', updated_at = now()
	           WHERE id = $1::uuid`
	tag, err := s.q.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("store: tombstone ownership: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListOwnershipByTenant implements OwnershipStore (tenant filter in SQL).
func (s *PgStore) ListOwnershipByTenant(ctx context.Context, tenantID string) ([]ResourceOwnership, error) {
	const q = `SELECT ` + ownershipColumns + ` FROM resource_ownership
	           WHERE tenant_id = $1::uuid ORDER BY vmid`
	return s.listOwnership(ctx, q, tenantID)
}

// ListOwnershipByProject implements OwnershipStore.
func (s *PgStore) ListOwnershipByProject(ctx context.Context, projectID string) ([]ResourceOwnership, error) {
	const q = `SELECT ` + ownershipColumns + ` FROM resource_ownership
	           WHERE project_id = $1::uuid ORDER BY vmid`
	return s.listOwnership(ctx, q, projectID)
}

func (s *PgStore) listOwnership(ctx context.Context, q, arg string) ([]ResourceOwnership, error) {
	rows, err := s.q.Query(ctx, q, arg)
	if err != nil {
		return nil, fmt.Errorf("store: list ownership: %w", err)
	}
	defer rows.Close()
	out := []ResourceOwnership{}
	for rows.Next() {
		o, err := scanOwnership(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan ownership: %w", err)
		}
		out = append(out, *o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list ownership: %w", err)
	}
	return out, nil
}

// ListActiveVMIDs implements OwnershipStore: the set of VMIDs with a live
// ownership row (active or pending).
func (s *PgStore) ListActiveVMIDs(ctx context.Context) (map[int]bool, error) {
	const q = `SELECT vmid FROM resource_ownership WHERE status IN ('active', 'pending')`
	rows, err := s.q.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: list active vmids: %w", err)
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var vmid int
		if err := rows.Scan(&vmid); err != nil {
			return nil, fmt.Errorf("store: scan vmid: %w", err)
		}
		out[vmid] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list active vmids: %w", err)
	}
	return out, nil
}

// ListMembershipsByScope implements MembershipStore (scope filter in SQL).
func (s *PgStore) ListMembershipsByScope(ctx context.Context, scopeType, scopeID string) ([]Membership, error) {
	const q = `SELECT id::text, user_id::text, scope_type, scope_id::text, role, created_at, updated_at
	           FROM memberships WHERE scope_type = $1 AND scope_id = $2::uuid ORDER BY created_at`
	rows, err := s.q.Query(ctx, q, scopeType, scopeID)
	if err != nil {
		return nil, fmt.Errorf("store: list memberships by scope: %w", err)
	}
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.ID, &m.UserID, &m.ScopeType, &m.ScopeID, &m.Role, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan membership: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list memberships by scope: %w", err)
	}
	return out, nil
}

// ListMembershipsByScopes implements MembershipStore: one query over a batch of
// scope ids (scope_id = ANY), replacing a per-scope fan-out.
func (s *PgStore) ListMembershipsByScopes(ctx context.Context, scopeType string, scopeIDs []string) ([]Membership, error) {
	out := []Membership{}
	if len(scopeIDs) == 0 {
		return out, nil
	}
	const q = `SELECT id::text, user_id::text, scope_type, scope_id::text, role, created_at, updated_at
	           FROM memberships WHERE scope_type = $1 AND scope_id = ANY($2::uuid[]) ORDER BY created_at`
	rows, err := s.q.Query(ctx, q, scopeType, scopeIDs)
	if err != nil {
		return nil, fmt.Errorf("store: list memberships by scopes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.ID, &m.UserID, &m.ScopeType, &m.ScopeID, &m.Role, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan membership: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list memberships by scopes: %w", err)
	}
	return out, nil
}

// GetEffectiveRoles implements MembershipStore. One tenant-filtered membership
// scan yields the tenant role and the per-project roles; the max-role reduction
// happens in Go via roleRank.
func (s *PgStore) GetEffectiveRoles(ctx context.Context, userID, tenantID string) (string, map[string]string, error) {
	const q = `
		SELECT m.scope_type, m.scope_id::text, m.role
		FROM memberships m
		LEFT JOIN projects p ON m.scope_type = 'project' AND p.id = m.scope_id
		WHERE m.user_id = $1::uuid
		  AND (
		    (m.scope_type = 'tenant'  AND m.scope_id = $2::uuid)
		    OR (m.scope_type = 'project' AND p.tenant_id = $2::uuid)
		  )`
	rows, err := s.q.Query(ctx, q, userID, tenantID)
	if err != nil {
		return "", nil, fmt.Errorf("store: get effective roles: %w", err)
	}
	defer rows.Close()

	tenantRole := ""
	projectRoles := map[string]string{}
	for rows.Next() {
		var scopeType, scopeID, role string
		if err := rows.Scan(&scopeType, &scopeID, &role); err != nil {
			return "", nil, fmt.Errorf("store: scan effective role: %w", err)
		}
		switch scopeType {
		case "tenant":
			if roleRank(role) > roleRank(tenantRole) {
				tenantRole = role
			}
		case "project":
			if roleRank(role) > roleRank(projectRoles[scopeID]) {
				projectRoles[scopeID] = role
			}
		}
	}
	if err := rows.Err(); err != nil {
		return "", nil, fmt.Errorf("store: get effective roles: %w", err)
	}
	return tenantRole, projectRoles, nil
}
