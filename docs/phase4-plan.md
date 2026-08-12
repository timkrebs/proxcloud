# Phase 4 — Quotas + Audit: build spec

Branch: `feat/multi-tenancy`. Builds on Phases 1–3 (committed, green). Implements
the enforcement half of ADR-0009 (quotas/reservation) and ADR-0010 (audit write +
stale-pending reconciler), with the refinements in **ADR-0012** (tenant-level
lock, reserved columns, audit intent+outcome, provisioned-disk quota). The wire
contract is `docs/api/phase4-quotas-audit.md` — do not deviate from the shapes
there. This is an execution spec for backend-engineer and frontend-engineer.

Fills existing seams only — **no middleware or router restructure**:
`AuditOnMutation` (authz/middleware.go), the `CreateOwnership` call in
`handlers/create.go`, and the engine finalize/release hooks are already mounted;
Phase 4 fills their bodies and adds store methods behind the existing interfaces.

---

## 0. Decisions that need Tim (flagged — each already chosen so no plan is blocked)

1. **Who sets quotas.** Tenant quota = **platform-admin only** (`PUT /api/admin/
   tenants/{id}/quota`); a tenant Owner must not raise their own cap. Project
   quota = tenant **Owner** (`PUT …/projects/{id}/quota`), capped per-dimension by
   the tenant limit. **Confirm.**
2. **Disk quota = provisioned (`MaxDisk`) not actual (`Disk`)** — ADR-0012 §4.
   Deterministic, wizard-showable, prevents thin-provisioning past the cap.
   **Confirm** (vs. billing on actual bytes).
3. **Audit fail-closed model = intent-before + outcome-after, one row** — ADR-0012
   §3. Pre-insert failure → 500, mutation never runs; post-update failure → loud
   log, no 500 (intent row already durable). **This amends ADR-0010's literal
   "insert-only"** (adds a one-way outcome finalize on the middleware's own row).
   **Needs sign-off** because it amends an accepted ADR.
4. **Advisory lock keyed on TENANT, not project** — ADR-0012 §1. Closes the
   cross-project tenant-cap race. **Amends ADR-0009's `pg_advisory_xact_lock
   (project_id)`. Needs sign-off.**
5. **Migration 000002 adds `reserved_vcpu/ram_mb/disk_gb` to
   `resource_ownership`** so pending reservations are countable (ADR-0012 §2).
   Schema addition beyond migration 000001. **Confirm.**
6. **Phase-4 reconciler = stale-pending sweep ONLY.** Drift handling (PVE-but-
   unknown / DB-but-gone tombstone, Unassigned view) stays Phase 6 per the master
   plan. Quota correctness does not depend on it: an active row counts only if its
   VMID is in the live snapshot, so a guest deleted through Proxcloud stops
   counting on the next refresh. **Confirm** that synchronous delete→tombstone is
   deferred.
7. **Project quota ≤ tenant quota per-dimension enforced on PUT; sum-of-projects
   ≤ tenant NOT enforced** (tenant check at create is the backstop). **Confirm.**
8. **Over-quota HTTP status = `409 quota_exceeded`** (distinct from the VMID
   `409 conflict`). Stated, not blocking.

---

## 1. Quota model, store & usage

### 1.1 Schema — migration `000003_reserved_alloc` (backend-engineer)
```sql
-- up
ALTER TABLE resource_ownership
  ADD COLUMN reserved_vcpu    integer,
  ADD COLUMN reserved_ram_mb  bigint,
  ADD COLUMN reserved_disk_gb bigint;
CREATE INDEX resource_ownership_pending_created_idx
  ON resource_ownership (created_at) WHERE status = 'pending';
-- down: drop index, drop the three columns.
```
`quotas` and `audit_log` already exist (000001) — do **not** recreate. Extend the
`store.ResourceOwnership` struct + `CreateOwnershipParams` with the three
`*int/*int64` reserved fields (nullable).

### 1.2 QuotaStore sub-interface (`store.go` + `postgres.go` + `storetest/fake.go`)
```go
type Alloc struct { VCPU int; RAMMB int64; DiskGB int64 } // PVE-free; handler fills from ClusterResources

type QuotaStore interface {
    GetQuota(ctx, scopeType, scopeID string) (*Quota, error)          // ErrNotFound ⇒ caller treats as all-unlimited
    UpsertQuota(ctx, UpsertQuotaParams) (*Quota, error)               // INSERT … ON CONFLICT (scope_type,scope_id) DO UPDATE
    // ComputeUsage: one tenant-filtered SELECT of active+pending rows, aggregated
    // in Go against the snapshot. Active row → snapshot[vmid] (absent ⇒ 0);
    // pending row → reserved_*. Returns tenant total + per-project breakdown.
    ComputeUsage(ctx, tenantID string, snapshot map[int]Alloc) (tenant QuotaUsage, byProject map[string]QuotaUsage, err error)
    // ReserveOwnership: the concurrency-safe reservation (see §2). Runs its own
    // WithTx + AdvisoryLock(AdvisoryKeyTenant(tenantID)); re-reads pending usage
    // under the lock; checks project AND tenant limits; inserts the pending row.
    ReserveOwnership(ctx, ReserveOwnershipParams) (*ResourceOwnership, error) // ErrQuotaExceeded | ErrConflict
    // Audit choke-point store ops (§4) — the ONLY audit mutations.
    InsertAuditIntent(ctx, AuditIntent) (id string, err error)
    FinalizeAudit(ctx, id, outcome string, detail []byte) error
    ListAudit(ctx, AuditQuery) ([]AuditEntry, error)                  // tenant-filtered, keyset by (ts,id)
}

type ReserveOwnershipParams struct {
    TenantID, ProjectID string
    VMID int; GuestType, Node string; CreatedBy *string
    Reserved Alloc                 // requested delta (§2.2)
    Snapshot map[int]Alloc         // active allocations, fetched before the lock
}
type ErrQuotaExceeded struct { Scope, Dimension string; Limit, Used, Requested int64 } // implements error; handler → 409
```
`AdvisoryKeyTenant(tenantID string) int64` = `int64(fnv1a64(tenantID))` — new
helper in `store`. `storetest.Fake.ReserveOwnership` computes usage in-memory and
enforces the same checks so handler tests pass; the *lock* is only meaningful
against real Postgres (see §7 race test).

### 1.3 Usage rollup (exact)
- **vCPU** += active `MaxCPU` (cores) | pending `reserved_vcpu`.
- **RAM MB** += active `MaxMem`/(1024·1024) | pending `reserved_ram_mb`.
- **Disk GB** += active `MaxDisk`/(1024³) | pending `reserved_disk_gb` (provisioned; ADR-0012 §4).
- **Count** += 1 per active row present in snapshot, +1 per pending row.
- Project usage = rows where `project_id=$p`; **tenant usage = all tenant rows**
  (project sums roll up into tenant). Both limits enforced at create.

---

## 2. Reservation flow (concurrency-safe) — `handlers/create.go` + engine (backend-engineer)

Replace the direct `d.Store.CreateOwnership(...)` block (create.go lines ~82–110)
with the reservation. **Order (enforced BEFORE any Proxmox create call):**

1. Resolve project→pool (existing), validate (existing), clone-source ownership
   (existing).
2. **Fetch `ClusterResources()` once** → build `snapshot map[int]store.Alloc`.
   (Also the source of the clone delta, step 2.2.)
3. **`req.Reserved`** = the requested delta (§2.2).
4. **`own, err := d.Store.ReserveOwnership(ctx, ReserveOwnershipParams{…, Snapshot: snapshot})`.**
   Inside the store, within `WithTx`:
   a. `AdvisoryLock(ctx, AdvisoryKeyTenant(tenantID))` — held for two SQL stmts.
   b. `SELECT vmid, project_id, status, reserved_* FROM resource_ownership
      WHERE tenant_id=$t AND status IN ('active','pending')`.
   c. Aggregate `tenantUsage` + `projectUsage[$p]` per §1.3 (active←snapshot,
      pending←reserved).
   d. Load tenant quota + project quota; for each non-null dimension check
      `usage + delta ≤ limit`; on the first violation → `ErrQuotaExceeded`
      (rollback releases the lock).
   e. `INSERT` the pending row with `reserved_*` set; unique-VMID clash →
      `ErrConflict`. Commit (releases the lock).
5. Handler error mapping: `ErrQuotaExceeded` → **409 `quota_exceeded`** (message
   names Scope+Dimension+Used/Limit); `ErrConflict` → **409 `conflict`**; other →
   500. **On any of these, no Proxmox call has run.**
6. Ensure pool (existing) → `req.Pool` → `Deploy.Submit` (existing). Engine
   finalize (pending→active, keeps `reserved_*`) / release (delete) are already
   wired — unchanged; a released reservation frees quota immediately.

### 2.1 The advisory lock defeats the races
- **Intra-project parallel creates:** serialized read-modify-write; pending rows
  inserted by an earlier winner are read by the next under the same lock.
- **Cross-project, same tenant:** the lock is per-**tenant**, so both creates
  serialize on the tenant quota (ADR-0012 §1). Multi-instance safe (Postgres
  advisory lock, not an in-process mutex).

### 2.2 Requested delta
- **create:** `{VCPU: req.Cores, RAMMB: req.MemoryMb, DiskGB: req.DiskGb, count:1}`.
- **clone:** copy the source template's allocation from `snapshot[cloneVmid]`
  (`MaxCPU`, `MaxMem`→MB, `MaxDisk`→GB), count 1. Linked clones conservatively
  count the full template disk (ADR-0012 §4).

### 2.3 Stale reservation reclaim — `internal/reconciler` (backend-engineer)
New goroutine started in `main.go` after backfill, before/alongside serve;
interval `cfg.ReconcilerInterval` (exists, default 5m). Add `cfg.ReservationTTL`
(new, default **45m** > the 30m clone `stepTimeout` + margin). Each tick:
`SELECT id FROM resource_ownership WHERE status='pending' AND created_at <
now()-$ttl` → `ReleaseOwnership(id)` + an audit row (`action:"reservation.reclaimed"`,
actor null/"system", outcome success). Frees quota leaked by a backend that died
mid-create. **Phase-4 reconciler does nothing else** (drift → Phase 6).

---

## 3. Quota API handlers — `internal/handlers/quotas.go` (backend-engineer)
Per `docs/api/phase4-quotas-audit.md`:
- `GetTenantQuota` (Reader) and `GetAdminTenantQuota` (Admin): fetch
  ClusterResources→snapshot, `ComputeUsage`, `GetQuota("tenant",id)` → `QuotaWithUsage`.
- `GetProjectQuota` (Reader): `ComputeUsage` once → build `ProjectQuotaResponse`
  (project breakdown + tenant total) so the wizard binds on the tighter remaining.
- `PutProjectQuota` (Owner): validate each limit ≤ tenant limit (400 otherwise),
  `UpsertQuota("project",id)`.
- `PutAdminTenantQuota` (Admin): `UpsertQuota("tenant",id)`.
Register all six routes (§ contract) in `authz/permissions.go`; the two admin
routes mount under `MountAdmin`, the four tenant routes under `MountTenant`.

---

## 4. Audit write — fill `AuditOnMutation` (backend-engineer)
Fill the existing stub body (no signature/mount change). New
`internal/authz/audit_actions.go` holds the static action-map (§ contract) +
`AuditAction(method, pattern, urlParams) string`. Middleware body:
1. Non-GET only (existing self-gate). Read identity + scope from context.
2. Derive action (map; missing entry → 500 + loud log; the completeness test
   prevents shipping that), target_type/target_id from path params.
3. **`id := store.InsertAuditIntent({actor, tenant, project, action, targetType,
   targetId, ip, outcome:"pending"})`.** On error → `writeErr(500)`; **return
   without calling `next` (fail-closed — nothing mutates).**
4. Wrap `w` with `middleware.NewWrapResponseWriter(w, r.ProtoMajor)` (as
   `accessLog` does); `next.ServeHTTP(ww, r)`.
5. `outcome = map(ww.Status())` (2xx success · 4xx denied · 5xx error);
   `detail = {status, upid?}`. `store.FinalizeAudit(id, outcome, detail)`; on
   error **log ERROR, do not 500** (intent row is durable → no unlogged mutation).
Optional `audit.Annotate(ctx,k,v)` context hook lets `CreateGuest` add
`vmid`,`name` to detail — never load-bearing for the one-row guarantee.

**`audit_completeness_test.go`** (mirrors permissions test): walk the real router;
for every non-GET pattern on the tenant subtree assert (a) it is wrapped by
`AuditOnMutation` and (b) `AuditAction` returns non-empty. CI fails on a gap.

---

## 5. Activity log — `internal/handlers/activity.go` (backend-engineer)
`GET /api/tenants/{tenantId}/activity` (Reader) per § contract. Spine =
`ListAudit(tenantID, before, limit)`; overlay = `ClusterTasks` filtered via the
existing `tenantOwnedVMIDs` + `taskSummary`, normalized to `ActivityEntry` for the
window `[oldestAuditTs, before)`. Resolve actor display names via
`ListUsersByIDs` (no N+1); project names via the tenant's projects map. Merge,
sort `ts` DESC, truncate to `limit`, set `NextBefore`. Register the route.

---

## 6. Frontend (frontend-engineer)
- **`lib/api/queryKeys.ts`** — add `qk.quota(tenantId)`,
  `qk.projectQuota(tenantId, projectId)`, `qk.activity(tenantId, filters)`.
- **`lib/api/client.ts`/hooks** — `getTenantQuota`, `getProjectQuota`,
  `putProjectQuota`, `putTenantQuota` (admin), `getActivity`. Tenant-prefixed.
- **`lib/api/mutations.ts`** — after a successful guest create/delete, invalidate
  `qk.quota` + `qk.projectQuota` so bars refresh. Map a `409 quota_exceeded`
  response to a distinct inline error (not the generic conflict toast).
- **`QuotaBars` component** (new, design-token bars; unlimited ⇒ "Unlimited", no
  bar) — used on the **tenant dashboard** (`qk.quota`) and each **project view**
  (`qk.projectQuota`). Loading skeleton / empty / error per DoD.
- **Wizard** — `BasicsTab` shows inline remaining quota once a project is picked
  (from `getProjectQuota`, min(project,tenant) remaining). The **sizing tab**
  validates `cores/memoryMb/diskGb` against remaining in
  `wizardStore.validateWizard`; block Next/Create with a clear message when over.
- **Quota config screen** — Owner edits project quotas (`putProjectQuota`);
  platform-admin edits tenant quotas (`putTenantQuota`). Owner UI hidden for
  Reader/Contributor; tenant-quota editor admin-only.
- **Activity Log screen** — new tenant nav item; table from `qk.activity` with a
  source badge (Proxcloud vs Proxmox), outcome chip, actor, target, timestamp;
  "Load more" advances `?before=nextBefore`. Filters: source, project, outcome.

---

## 7. New/changed files, sequencing, acceptance

### New (backend)
`backend/migrations/000003_reserved_alloc.{up,down}.sql`;
`backend/api/types/{quota.go,activity.go}`;
`internal/handlers/{quotas.go,activity.go}`;
`internal/authz/audit_actions.go`; `internal/reconciler/reconciler.go` (+ test);
`internal/authz/audit_completeness_test.go`.
### Changed (backend)
`internal/store/{store.go,postgres.go,storetest/fake.go}` (QuotaStore, Alloc,
Reserve/Audit ops, AdvisoryKeyTenant, ResourceOwnership reserved fields);
`internal/authz/{middleware.go (AuditOnMutation body),permissions.go (6 routes)}`;
`internal/handlers/{create.go (reservation),handlers.go (MountTenant/Admin +6),
guest_actions.go? (optional annotate)}`; `internal/config/config.go`
(`ReservationTTL`); `backend/cmd/proxcloud/main.go` (start reconciler).
### New/changed (frontend)
new `QuotaBars`, quota-config screen, activity-log screen; changed
`queryKeys.ts`, `client.ts`, `mutations.ts`, wizard `BasicsTab`+sizing tab
+`wizardStore`, tenant dashboard, project view.

### Sequencing (within Phase 4)
1. **Schema + store** — migration 000002; QuotaStore + Alloc + ComputeUsage +
   ReserveOwnership + audit ops + AdvisoryKeyTenant; fake double. `go test ./...`
   green, no behavior change yet.
2. **Reservation in create path** — wire ReserveOwnership + 409 mapping; delta
   for create/clone; ClusterResources snapshot.
3. **Audit write** — fill AuditOnMutation (intent+outcome) + action-map +
   completeness test.
4. **Reconciler** — stale-pending sweep + config TTL + main.go wiring.
5. **Quota + activity APIs** — handlers + registry + contract types.
6. **Frontend** — quota bars, wizard remaining + validation, quota config,
   activity log.
7. **Tests + live demo** — over-quota create refused pre-PVE; parallel-create
   race respects the cap; every action in the activity log.

### Security acceptance criteria (hand to security-reviewer)
- **Quota non-bypassable under concurrency:** a real-Postgres test (mirror
  `postgres_auth_test.go`'s bootstrap race — the fake's no-op lock cannot prove
  it) launches N parallel `ReserveOwnership` in one project with `max_count=M` ⇒
  exactly M succeed, N−M `ErrQuotaExceeded`. A second test races two projects of
  one tenant against the **tenant** cap (proves the tenant-level lock).
- **Enforced before PVE:** over-quota `POST …/guests` returns 409 and the mock
  PVE `CreateVM`/`CreateLXC`/`CloneGuest` is asserted **never called**.
- **Pending counts:** a create in flight (pending row) is included in usage; a
  second create over the combined cap is refused.
- **No mutation without an audit entry:** structural — `audit_completeness_test`
  green (every mutating tenant pattern wrapped + action-mapped). Behavioral — a
  successful `project.create` leaves exactly one audit row; a forced
  `InsertAuditIntent` failure ⇒ 500 **and no project created** (fail-closed);
  a forced `FinalizeAudit` failure ⇒ 200 preserved + a `pending` row remains
  (no unlogged mutation) + loud log.
- **Quota authz:** tenant-quota PUT rejects non-admin (403); project-quota PUT
  rejects non-Owner (403) and rejects a limit exceeding the tenant limit (400).
- **Stale reclaim frees quota:** a pending row past `ReservationTTL` is released
  by the reconciler and its capacity returns to usage.
- **No cross-tenant leak:** `activity` and quota reads/usages filter by tenant
  **in SQL**; a cross-tenant `{projectId}` on a quota route → 404.
