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
