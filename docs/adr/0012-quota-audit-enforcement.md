# ADR-0012: Phase-4 quota & audit enforcement refinements

Date: 2026-08-12 · Status: proposed (amends ADR-0009, ADR-0010 — needs Tim's sign-off)

## Context

ADR-0009 fixed the reservation pattern and ADR-0010 the audit choke-point, but
both were written before the create path and `resource_ownership` schema were
built. Filling the Phase-3 seams surfaced four implementation-level questions
whose obvious readings are subtly wrong. This ADR records the refinements so the
engineers build the *correct* thing, and flags where each amends an accepted ADR.

## Decision

1. **Advisory lock is keyed on the TENANT, not the project.** ADR-0009 said
   `pg_advisory_xact_lock(project_id)`. A per-project lock serializes creates
   *within* a project but not *across* projects of the same tenant — two creates
   in projects A and B each fit their project quota and each read tenant usage
   below the tenant cap, both reserve, and together exceed the tenant cap. Since
   tenant usage is the superset of project usage, a single per-tenant lock
   serializes the read-modify-write for **both** the project and the tenant
   check. Key derivation: `AdvisoryKeyTenant(tenantID) = int64(fnv1a64(tenantID))`
   (a distinct keyspace from the fixed bootstrap key `0x70726f7863`; UUID hash
   collisions only cause occasional extra serialization, never incorrectness).

2. **Pending reservations carry their reserved allocation in three new
   columns.** A pending create has no PVE guest yet, so `ClusterResources`
   returns nothing for its VMID — it cannot be counted from the live snapshot,
   which would let a burst of parallel pending creates each count as zero and all
   pass. Migration `000002` adds nullable `reserved_vcpu`, `reserved_ram_mb`,
   `reserved_disk_gb` to `resource_ownership`, set at reservation time. Usage of
   an **active** row reads the live `ClusterResources` snapshot (0 if the VMID is
   absent — a deleted or not-yet-visible guest); usage of a **pending** row reads
   `reserved_*`. This keeps active usage tracking PVE truth (so a guest deleted
   through Proxcloud stops counting on the next snapshot with no synchronous
   tombstone) while pending reservations are always enforced.

3. **Audit is written as intent-before + outcome-after, one row.** ADR-0010's
   "insert-only, fail the mutation closed" cannot both hold and avoid 500-ing a
   PVE-succeeded create if written only after the response (the side effect is
   already committed; the client can't be un-mutated, and re-inserting can't
   help). Refinement: `AuditOnMutation` (a) inserts an intent row
   `outcome='pending'` **before** the handler — if that insert fails it returns
   500 and the handler never runs (**true fail-closed: nothing is mutated**);
   (b) captures the HTTP status via a response wrapper and finalizes the same
   row's outcome/detail **after** — if that update fails it logs loudly but does
   **not** 500 (the intent row is already a durable record, so there is no
   *unlogged* mutation). The only permitted audit mutations are
   `InsertAuditIntent` and a one-way `FinalizeAudit(id, outcome, detail)` on the
   middleware's own row; no general UPDATE/DELETE is exposed, so who/what/when is
   still immutable and rows are never removed.

4. **Disk quota counts provisioned capacity (`MaxDisk`), not actual bytes
   (`Disk`).** Provisioned is deterministic at create time (the wizard can show
   the exact delta), reflects the capacity a tenant has committed, and prevents
   thin-provisioning past the cap and then filling it. Linked clones
   conservatively count the full template disk.

## Consequences

- The reservation critical section is one `SELECT` + one `INSERT` under a
  per-tenant lock; `ClusterResources` is fetched once *before* the lock and
  passed in, so no PVE round-trip is ever held under the lock (ADR-0009's core
  goal, preserved).
- Every mutation is guaranteed a durable audit row *or* a 500 — never a silent
  unlogged success. Volume is one row per mutation (not two).
- Usage is a Go aggregation over one tenant-filtered ownership `SELECT` joined
  in memory to the `ClusterResources` snapshot; there is no drift-prone counter.
- A just-finalized guest can briefly undercount for the seconds before it appears
  in the cluster-resource snapshot — bounded, safe-direction-only, self-healing.

## Alternatives considered

- **Keep the project-level lock + a separate tenant lock:** two locks per create,
  more deadlock surface, no benefit over one tenant lock (tenant ⊇ project).
- **Store reserved allocation in a sidecar `reservations` table:** an extra join
  and lifecycle to keep in sync with the ownership row it shadows; three nullable
  columns on the row that already exists are simpler.
- **Two audit rows (intent + outcome):** doubles volume and complicates the
  activity feed; one row with a controlled one-way finalize is tighter and still
  immutable in every field that matters.
- **Actual-bytes disk quota:** non-deterministic at create time, un-showable in
  the wizard, and lets a tenant over-provision then fill up past the cap.

## Addendum (2026-09-24): growth reservations

Quota was enforced only when a guest was created; growing an existing guest
(disk resize, cores/memory change, snapshot rollback to a larger
configuration) now goes through `ReserveGuestGrowth`, under the same
per-tenant advisory lock as a create reservation (findings register H5 and
second review round H-A, `docs/security/audit-2026-08-14.md`):

- Every charge is measured against the guest's **effective footprint** —
  max(live snapshot, stored `reserved_*`) per dimension, the same rule
  `ComputeUsage` applies — so a request is charged for what exceeds what the
  tenant already pays for.
- **vCPU and RAM are absolute targets** (the charge is target − effective, or
  nothing). vCPU is sockets × cores, the unit PVE's `maxcpu` reports.
- **A disk resize is an additive delta**, measured against the named disk's
  own configured size and charged in full. It is never turned into an absolute
  target: the counted disk footprint is the boot disk plus every prior growth
  reservation, not the size of any one disk — deriving a target from one disk
  is what let repeated data-disk grows go uncharged.
- When the checks pass, effective + charge is written to the ownership row's
  `reserved_*` columns in the same transaction, **monotonically** (SQL
  `GREATEST`, which ignores NULLs): a reservation can only rise, so no later
  or racing request can reopen headroom another grow depends on.
  `ComputeUsage` counts an active row at max(live, reserved), so the grown
  footprint is charged before Proxmox reflects it.
- A rollback reads the snapshot's stored sizing and **fails closed** when it
  cannot be read; a container snapshot with no cores (no CPU limit at all) is
  refused.

Consequences: accounting can only over-count. Reservations are never cleared,
so after a shrink a tenant stays charged at the larger size until a reconciler
pass clears reservations the guest's real configuration no longer needs (open
item R8 in the register); the base size of non-boot disks stays outside usage
totals (R4). Rejected along the way: a check without a reservation (the
check-then-act race), an absolute disk target (the repeated-grow bypass), and
persisting max(live, target) (a later request could lower the reservation).
