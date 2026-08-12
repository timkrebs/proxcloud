# ADR-0005: Postgres datastore + repository layer

Date: 2026-08-12 · Status: accepted

## Context

Multi-tenancy needs a durable system of record for identities, tenants,
projects, memberships, ownership, quotas, sessions, and audit — none of
which exist in v1 (state lived in env vars and an in-memory task
registry). Two forces dominate the choice: the quota **reservation
pattern** (ADR-0009) needs real transactions and row/advisory locks that
survive concurrent creates, and the deployment is expected to grow to
more than one backend instance, so state must be reachable over the
network rather than pinned to one process's local file.

## Decision

- **PostgreSQL is the system of record**, added as a new
  docker-compose service. Access via `github.com/jackc/pgx/v5` with
  `pgxpool` for pooled, concurrency-safe connections.
- **Schema owned by `golang-migrate`**, migrations embedded with
  `//go:embed backend/migrations/*.sql` and run on startup *before the
  server accepts traffic*. Idempotent and versioned; a fresh Postgres and
  a re-run converge to the same schema.
- All data access sits behind a **`store.Store` interface** (sub-interfaces
  per aggregate: users, tenants, projects, memberships, ownership, quotas,
  sessions, audit) in a new `internal/store` package, with a pgx
  implementation and a test double. Transactions run through
  `WithTx(ctx, func(Store) error)` so a handler composes multi-table
  writes without leaking `pgx.Tx` into business logic.
- `store.Store` threads into `handlers.Deps` and is constructed in
  `main.go` step 8, mirroring how `proxmox.Client` is wired today.

## Consequences

- One typed data layer, mockable in tests exactly like the existing
  `ProxmoxClient` interface — the quota, authz, and audit logic is
  unit-testable without a live database.
- Postgres over **SQLite**: SQLite's single-writer lock and file-local
  storage would serialize the reservation path and block a second backend
  instance; Postgres advisory locks and MVCC give per-project concurrency
  and network access with a proven driver. The cost is one more service to
  run and back up — acceptable for a compose-based homelab product.
- Existing installs gain a hard dependency on `DATABASE_URL` and a
  first-boot migration/backfill; the upgrade path is the runbook in
  `docs/migration-multitenancy.md`. Migrations failing on boot is a
  deliberate fail-closed: the server does not serve on a half-built schema.
