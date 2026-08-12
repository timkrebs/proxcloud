# ADR-0010: Audit log & reconciliation

Date: 2026-08-12 · Status: accepted

## Context

A multi-tenant control plane must answer "who did what, where, and did it
succeed" for every state change, and it must cope with Proxmox drifting
out of sync with Proxcloud's ownership map (a VM created directly in the
PVE UI, or a guest deleted outside Proxcloud). Both need to be structural
guarantees, not per-handler discipline that one forgotten call breaks.

## Decision

- **Append-only `audit_log`** written at **one structural choke-point:**
  an `audit` middleware wrapping the mutating (non-GET) tenant-scoped
  route group. It records `actor_user_id`, `tenant_id`, `project_id`,
  `action`, `target_type`/`target_id`, `outcome`, `ip`, and a `detail`
  jsonb — sourced from request context plus the response status. Because
  it wraps the whole mutating group, **no mutation can complete without an
  audit entry**; a test asserts every mutating route pattern is covered.
- The table is **insert-only** (no update/delete path in the store
  interface); corrections are new rows, preserving the trail.
- **Reconciler goroutine** (configurable interval) diffs `ClusterResources`
  against `resource_ownership`:
  - **PVE-but-unknown** guest → surfaced in a platform-admin **Unassigned
    resources** view to be claimed into a project (reuses the bootstrap
    claim logic).
  - **DB-but-gone** ownership → marked **`tombstoned`** with an audit
    entry, not deleted.
  - Stale `pending` reservations past their TTL (ADR-0009) are released.
- **The reconciler never auto-deletes** on either side — it only reports,
  tombstones, and reclaims reservations.

## Consequences

- The activity log the UI shows is the audit table merged with the
  verbatim PVE task feed (ADR-0004), giving both Proxcloud-intent and
  cluster-truth in one timeline.
- Audit writes are best-effort-durable but on the request path; a failed
  audit insert fails the mutation closed (no silent unlogged change),
  consistent with the "no mutation without an audit entry" rule.
- Drift is visible and reversible rather than destructive: an admin
  decides what an unassigned or tombstoned resource means. Auto-remediation
  (auto-claim, auto-add-to-pool on migration) is deferred to a later phase.
