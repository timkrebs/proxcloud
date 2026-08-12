# ADR-0009: Quotas & concurrency-safe reservation

Date: 2026-08-12 · Status: accepted

## Context

Tenants and projects need enforceable limits on vCPU, RAM, disk, and
resource count. Enforcement must happen **before** any Proxmox call (an
over-quota create should never touch PVE), and it must be correct under
concurrency: two simultaneous creates must not both pass a check that only
one has room for. A guest create is a slow, async PVE operation (tens of
seconds), so the naive "check then create inside one transaction" holds a
lock across a 30s network call — unacceptable.

## Decision

- **Per-tenant and per-project quotas** (`max_vcpu`, `max_ram_mb`,
  `max_disk_gb`, `max_count`; nullable = unlimited). Live usage is
  computed from **owned resources** — `resource_ownership` joined to
  `ClusterResources` for real allocations — not a running counter that
  could drift.
- **Reservation pattern under `pg_advisory_xact_lock(project_id)`:**
  1. A **short transaction** takes the per-project advisory lock,
     re-computes current usage (active + pending rows), checks the request
     fits both project and tenant limits, inserts a **`pending`
     `resource_ownership` reservation**, and **commits** — releasing the
     lock in milliseconds.
  2. The **slow Proxmox create runs outside the lock**, keyed to that
     pending row.
  3. On task success the row is **finalized** (`pending`→`active`, real
     `vmid`/`upid`); on failure or timeout it is **released**, freeing the
     reserved quota.

## Consequences

- Concurrent creates serialize only on the fast reservation, never on PVE:
  the advisory lock is held for a DB round-trip, not a guest build.
- Pending reservations count against usage, so a burst of parallel creates
  cannot collectively exceed the cap — the race the plan's acceptance test
  targets.
- **Why not the alternatives:** holding a tx across the PVE call would pin
  a connection and block the project for the whole build; an **in-process
  mutex** breaks the moment a second backend instance exists (ADR-0005);
  a **naive row lock** on a quota row still leaves the create outside any
  guaranteed-atomic check-and-insert. The advisory lock is
  Postgres-native, transaction-scoped (auto-released on commit/rollback,
  so a crashed backend can't wedge a project), and multi-instance safe.
- A released reservation must be reliably reclaimed even if the backend
  dies mid-create; the reconciler (ADR-0010) sweeps stale `pending` rows
  past a TTL so leaked reservations self-heal.
