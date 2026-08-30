# ADR-0029: Deployment-set abstraction (one catalog action → N linked guests)

Date: 2026-08-28 · Status: accepted · Provisioning/Service catalog · Tenancy-critical

## Context

Every catalog service shipped so far is `kind: single` — one action provisions
one guest (`services/postgresql/service.yaml:16`, "`set` is reserved for
multi-guest services (Phase E) and rejected by the loader in v1"). Phase E lands
the first multi-guest service (K3s, ADR-0030): one catalog action must create a
**cluster** — N guests that are provisioned together, share one status, start and
stop and delete together, and are quota-accounted as a unit. The single-guest
machinery does not express any of that:

- **Quota is per-guest.** `checkQuota` hardcodes one unit of count
  (`quota.go:180`, `usage.Count+1 > *q.MaxCount`) and `ReserveOwnership`
  (`quota.go:190`) reserves exactly one `resource_ownership` row under one
  `AdvisoryLock(AdvisoryKeyTenant)` (`quota.go:193`). N sequential single
  reservations race each other's headroom and can half-commit: three of five
  members reserved, then the fourth trips the cap, leaving three phantom guests.
- **Status is in-memory and single-guest.** `Deployment` lives only in the
  engine's `runs` map (`engine.go:107`) and is explicitly non-durable
  ("Deployment not found — deployment progress does not survive a backend
  restart", `create.go:225`). A cluster that takes minutes to converge cannot
  hang its whole status off a map that a deploy or crash erases.
- **Lifecycle is per-VMID.** Schedules, TTL, start/stop, and ownership tombstones
  all key on a single `vmid`; nothing groups the members so that "stop the
  cluster" or "this set's TTL fired" fans out correctly.

We need a first-class **deployment set**: a durable grouping of member guests
that reuses the existing ownership/quota/tombstone/schedule machinery rather than
forking it.

## Decision

**A `deployment_set` is one catalog action that creates N linked guests sharing a
lifecycle. It is a thin durable grouping over the existing per-guest ownership
model — membership is a foreign key added to `resource_ownership`, not a parallel
table — plus one new atomic multi-guest reservation and one new SSE frame.**

### Data model (migration 000009)

New `deployment_set` table (conventions mirror 000001/000007 — UUID PK, CHECK
enums, the composite FK to `projects`):

```
deployment_set (
  id           uuid PK default gen_random_uuid()
  tenant_id    uuid NOT NULL
  project_id   uuid NOT NULL
  service_id   text NOT NULL                         -- e.g. 'k3s'
  status       text NOT NULL CHECK (status IN
                 ('provisioning','ready','degraded','failed','deleting'))
  teardown …   -- teardown-order metadata (see ADR-0030 reverse ordering)
  created_at / updated_at timestamptz
  FOREIGN KEY (tenant_id, project_id) REFERENCES projects (tenant_id, id)
)
```

The composite FK is the same isolation invariant every tenant-scoped table
carries (`000001_init.up.sql:78`): a set's tenant can never drift from its
project's tenant, so a write-path bug cannot mis-scope a set.

**Membership is added to `resource_ownership`, not a new member table.** Two
nullable columns — `deployment_set_id uuid` (FK → `deployment_set(id)`) and
`role text` (ADR-0030: `'server'` | `'agent'`) — plus a partial index
`WHERE deployment_set_id IS NOT NULL`. This is the load-bearing choice: every
existing ownership behaviour — `ComputeUsage`'s active+pending aggregation
(`quota.go:90`), the tombstone-revive `ON CONFLICT (vmid)` path
(`postgres.go:741`), the pending `reserved_*` snapshot columns (`000003`), the
stale-pending reconciler sweep, the `expired_at` TTL marker (`000007`) — keeps
working **unchanged** on a member row. A member is just an ownership row that
also names a set. A separate `deployment_set_member` table would fork all of
that: quota accounting, the tombstone lifecycle, the reconciler, and TTL would
each need a second code path. A nullable FK on the row that already exists is
strictly less surface.

Schedules extend the same way: the `scope` CHECK (`resource` | `project`,
`schedules.go`) gains `'set'`, with a nullable `set_id` and a partial unique
index `schedules_set_uidx (tenant_id, set_id) WHERE scope = 'set'` mirroring the
existing `schedules_resource_uidx` / `schedules_project_uidx`.

### Atomic multi-guest quota: `ReserveOwnershipBatch`

A new store method reserves **all N members or none**, under **one**
`AdvisoryLock(AdvisoryKeyTenant)` (the same per-tenant lock that already
serializes a tenant's reservation, `quota.go:193`):

1. Acquire the tenant advisory lock; run `ComputeUsage` **once** (`quota.go:90`).
2. Walk the N member deltas, **accumulating** vcpu/ram/disk **and count** across
   members into the running usage before each `checkQuota`. Because the per-guest
   `checkQuota` hardcodes `+1` on count (`quota.go:180`), the batch cannot reuse
   it N times against the same base usage — it must fold each accepted member
   into `usage` so member k is checked against base + members 0..k-1. The first
   member that would exceed any project or tenant dimension returns a single
   `ErrQuotaExceeded` and the whole transaction rolls back.
3. On success, insert N `pending` `resource_ownership` rows in the same tx, each
   tagged with the new `deployment_set_id` + `role`.

All-or-nothing, decided **before any Proxmox call** — same discipline as the
single-guest path, which reserves before `CreateVM` and releases on rejection
(`engine.go:240-262`). No partial cluster is ever reserved.

### Lifecycle: set-level operations fan out in teardown order

- **Start/stop** iterate the set's members in ADR-0030 order (server before
  agents on start; reverse on stop), reusing the existing per-guest
  `GuestAction`.
- **Delete** walks members in reverse teardown order and, per member, reuses the
  established single-guest teardown: destroy the guest, then
  `TombstoneOwnership` the row (`postgres.go:806`) — the tombstone frees the VMID
  for reuse exactly as today — and `RemoveSnippet` for that member (ADR-0025).
  The `deployment_set` row moves to `deleting` for the duration and is removed
  last.
- **TTL / auto-shutdown attach at the set level.** A new `MaterializeForSet`
  fan-out mirrors `AutoShutdown.MaterializeProject` (`autoshutdown.go:156`): it
  lists the set's member ownership rows and re-materializes each member's jobs,
  skipping tombstoned members and logging-not-failing a per-member error so one
  bad member never blocks the rest — the exact fan-out contract already proven
  for project schedules.

### Member-failure policy: fail the set, do not auto-destroy successes

If any member's provision fails, the set is marked **`failed`** and each member's
status is surfaced individually (which came up, which did not). **Successful
members are NOT auto-destroyed.** Deleting the set is the single cleanup path and
tears down everything (above). Rationale: auto-rollback would destroy a healthy,
already-quota-charged guest the operator can still inspect for the failure cause
(a half-joined K3s agent's logs are the diagnosis), and a destroy-on-failure path
is itself a second failure surface — a rollback that fails leaves worse state
than a clean `failed` set with honest per-member status. Leaving the set present
and honestly `failed`, with delete as the one teardown, is both more debuggable
and less code than compensating rollback. `degraded` is reserved for a set that
reached `ready` and later lost a member (post-provision health), distinct from a
provision-time `failed`.

### Durable status

Set status lives in the `deployment_set` **table**, not the engine's in-memory
`runs` map. A cluster provision outlives a backend restart: the row is the
truth, the per-member `resource_ownership` rows are the truth, and the Proxmox
task log is the truth — the same "durable truth lives outside the engine"
principle `engine.go:76-77` states for single guests, now with a DB row because a
set has status a single guest did not (a multi-minute, multi-member convergence).
`GET .../deployment-sets/{setId}` reads the table and reconstructs per-member
status; it does not depend on `runs`.

### SSE scoping: a new `deployment_set` frame, never a broadcast fall-through

`deliver()` (`handler.go:115`) gains a `case "deployment_set"`: platform-admin
passes; otherwise the frame is delivered only when **every** member VMID it names
is owned by the subscriber's active tenant (the set is atomic, so its members
share one tenant — checking membership against `owned` is a per-VMID lookup like
the existing `owned[dep.VMID]` deployment case, `handler.go:137`). This case
**MUST NOT fall through to the `default` broadcast** — the same hazard the
`schedule_warning` / `ttl_warning` cases already call out
(`handler.go:139-145,151-157`: "It must NOT fall through to the default broadcast
case, which would leak one tenant's guest activity to every subscriber"). A set
frame names N owning VMIDs; broadcasting it would leak a tenant's cluster
topology to every connection.

### 404-not-403 authorization

`{setId}` is a **tenant-level** identifier — it is not a `{vmid}` and is not
auto-resolved by `ResolveScope`/`ResolveOwnership`. So the handler does its own
tenant-filtered 404, copying `GetDeployment` (`create.go:212-236`): load the set,
confirm `set.tenant_id == identity.ActiveTenantID` (and, for member operations,
that each VMID resolves via `ResolveOwnership`), and return the tenancy iron
rule's **404, never 403** on any miss — no existence leak of another tenant's
set. **Reader** authorizes GETs; **Contributor** authorizes mutation (create /
start / stop / delete), consistent with the guest permission table. As with every
mounted route, `{setId}` routes ship with an `internal/authz` permission-table
entry (tenancy rule 2) and their mutations audit at the choke-point (tenancy
rule 3).

## Consequences

- One atomic reservation makes a cluster all-or-nothing at the quota gate: a
  tenant near its cap cannot half-provision a K3s set and strand three charged
  guests. The batch's count accumulation is a permanent constraint — any future
  edit to `checkQuota`'s hardcoded `+1` must keep the batch summing across
  members, or the cap leaks.
- Reusing `resource_ownership` for membership means the reconciler, tombstone
  revive, TTL `expired_at`, and pending-snapshot accounting all keep working on
  members with zero new code; the cost is two nullable columns on a hot table and
  a partial index, which is cheap.
- Set status survives a restart (unlike a single `Deployment`), so a long cluster
  provision and its post-provision `degraded` transitions are observable after a
  deploy — at the cost of a new table the single-guest path does not need.
- The new SSE frame is one more `deliver()` case that must stay out of the
  `default` branch forever; the existing warning-frame comments make that a
  known, enforced constraint rather than a latent leak.
- Member-failure leaves charged, running guests until the operator deletes the
  set — deliberate (debuggability over auto-cleanup), but it means a `failed` set
  still consumes quota until torn down, which the UI must make obvious.

## Alternatives considered

- **A separate `deployment_set_member` table** instead of columns on
  `resource_ownership`. Rejected: it forks every behaviour that already keys on an
  ownership row — quota (`ComputeUsage`), tombstone revive (`ON CONFLICT (vmid)`),
  the stale-pending reconciler, TTL `expired_at` — into a second, parallel code
  path that must be kept in lockstep. A nullable FK on the existing row reuses all
  of it. The grouping is a set membership, not a new resource.
- **N sequential single-guest `ReserveOwnership` calls** for a cluster. Rejected:
  each takes and releases the tenant lock independently, so concurrent creates
  race the same headroom, and a mid-sequence cap trip half-commits the set —
  exactly the phantom-guest failure the batch closes. One lock, one usage read,
  accumulated deltas is the only race-free shape (ADR-0009 §2 discipline).
- **In-memory set status** (extend the engine `runs` map). Rejected: a
  multi-minute, multi-member convergence cannot survive a deploy on a homelab
  product that redeploys often, and the single-guest map already warns it is
  non-durable (`create.go:225`). A set has real status; it gets a real row.
- **Auto-rollback on member failure** (destroy successful members). Rejected:
  destroys a healthy, quota-charged guest the operator needs to diagnose the
  failure, and adds a compensating-destroy path that is itself a failure surface.
  Honest `failed` status + delete-as-the-one-teardown is more debuggable and less
  code.
- **Broadcasting set progress on the existing `deployment` frame** (no new
  case). Rejected: a set frame names N owning VMIDs; routing it through a path
  scoped to a single `dep.VMID` either drops members or, worse, leaks the
  cluster's topology — the precise fall-through leak `handler.go:139-163` forbids.

See ADR-0030 (the K3s cluster this abstraction first carries — roles, sequencing,
join strategy), ADR-0025 (per-member snippet delivery and removal reused on
teardown), ADR-0028 (per-member `configuring` readiness the set aggregates into
`ready`/`degraded`), and ADR-0009/0012 (the quota concurrency and enforcement
model `ReserveOwnershipBatch` extends).
