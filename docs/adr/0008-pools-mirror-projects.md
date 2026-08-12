# ADR-0008: Proxmox pools mirror projects

Date: 2026-08-12 · Status: accepted

## Context

Projects are Proxcloud's resource-grouping unit; Proxmox's native
grouping primitive is the **resource pool**. v1 already plumbs an optional
`pool` at guest create (`create.go` → `p["pool"]`). To keep Proxmox a
faithful mirror of Proxcloud state — and to give pool-scoped PVE token
privileges something real to bind to — each project should own a pool.

## Decision

- On **project create**, create a Proxmox pool named
  `pc-<tenantslug>-<projslug>` and store the resolved id on the project
  row (`projects.pool_id`).
- **Slug rules:** lowercase, `[a-z0-9-]` only, other runs collapsed to a
  single `-`, truncated to fit PVE's pool-id length limit. On collision,
  append a numeric suffix (`-2`, `-3`, …) until unique; the *resolved*
  pool id is persisted, never recomputed, so a later name edit cannot
  drift the mapping.
- **Ensure-pool-exists** runs before every guest create (idempotent): if
  the project's pool is missing on PVE it is recreated, then the existing
  `p["pool"]` passthrough places the guest.
- New `Client` methods **`CreatePool` / `DeletePool` / `AddPoolMembers`**
  (raw `/pools` POST/PUT/DELETE, mirroring `internal/proxmox/create.go`),
  added to the `ProxmoxClient` interface and the `proxmoxtest` mock.

## Consequences

- Proxmox stays a readable mirror: `pc-<tenant>-<project>` is greppable in
  the PVE UI, and pool-scoped tokens become viable later.
- Project delete is gated on emptiness (no owned resources) before the
  pool is removed, so a `DeletePool` never orphans running guests.
- **Open items delegated to proxmox-specialist** (verify against `pve01`):
  the exact token privileges required (`Pool.Allocate` and any
  companions for pool membership edits); pool rename vs delete-and-recreate
  semantics; and that **pool membership is cluster-wide and survives node
  migration** (intended, unconfirmed) — if not, the reconciler (ADR-0010)
  must re-add members post-migration.
