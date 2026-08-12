package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

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
	const q = `SELECT id::text, tenant_id::text, name, slug, pool_id, created_at, updated_at
	           FROM projects WHERE tenant_id = $1 ORDER BY slug`
	rows, err := s.q.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	defer rows.Close()

	out := []Project{}
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.TenantID, &p.Name, &p.Slug, &p.PoolID, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: scan project: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	return out, nil
}
