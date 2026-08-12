# Proxcloud tenancy model

This document explains how Proxcloud isolates users and resources. It is
the human-readable companion to ADR-0007 (authorization), ADR-0008 (pools),
and ADR-0009 (quotas).

## The hierarchy

```
Tenant   (≙ Azure Directory)      an isolation boundary; platform-admins manage its lifecycle
└── Project   (≙ Resource Group)  ──►  Proxmox pool  pc-<tenant-slug>-<project-slug>
    └── Resource  (qemu / lxc)    ──►  ownership row  vmid → project → tenant
```

- A **Tenant** is the top-level isolation boundary. Nothing crosses it
  except platform-admin operations. In Proxmox terms the whole cluster is
  shared, but Proxcloud partitions it logically per tenant.
- A **Project** groups resources inside a tenant and maps 1:1 to a Proxmox
  **resource pool** (ADR-0008). Creating a project creates its pool;
  deleting a project (only when empty) deletes its pool.
- A **Resource** is a VM or LXC container. Every resource has an
  **ownership row** binding its `vmid` to exactly one project and tenant.
  Cross-tenant access to a `vmid` returns **404, never 403** — no
  existence leak.

## Users, memberships, and roles

- **Users are global identities.** One account can belong to several
  tenants; the top-bar **tenant switcher** chooses the active one.
- Access is granted by a **membership**: a `(user, scope, role)` triple
  where scope is a tenant or a project.
- **Roles:**
  - **Reader** — view resources, metrics, and project state.
  - **Contributor** — everything a Reader can do, plus create/start/stop/
    delete resources and manage snapshots.
  - **Owner** — everything a Contributor can do, plus manage members,
    quotas, and projects within their scope.
- **Platform-admin** is a separate account flag (not a role in the
  hierarchy). It governs cross-tenant operations and cluster-infrastructure
  views (Nodes, Storage, cluster capacity), which are hidden from ordinary
  tenant users.

## Role → permission matrix

| Capability | Reader | Contributor | Owner | Platform-admin |
|---|:---:|:---:|:---:|:---:|
| View resources & metrics | ✓ | ✓ | ✓ | ✓ |
| Create resource | | ✓ | ✓ | ✓ |
| Start / stop / reboot resource | | ✓ | ✓ | ✓ |
| Delete resource | | ✓ | ✓ | ✓ |
| Manage snapshots | | ✓ | ✓ | ✓ |
| Manage members & roles | | | ✓ | ✓ |
| Manage quotas | | | ✓ | ✓ |
| Create / rename / delete projects | | | ✓ | ✓ |
| View cluster infra (nodes/storage/capacity) | | | | ✓ |
| Tenant lifecycle & cross-tenant ops | | | | ✓ |
| Claim unassigned / reconcile drift | | | | ✓ |

Owner powers apply **only within the Owner's scope**. A project Owner
manages that project; a tenant Owner manages the whole tenant.

## Scope inheritance rules

- A **tenant role inherits to every project** in that tenant. A tenant
  Contributor is a Contributor in all its projects.
- A **project role can only add** privilege, never subtract. A tenant
  Reader who is also a project Contributor is a Contributor *in that
  project* and a Reader elsewhere.
- The **effective role** for any request is the **maximum** of the user's
  tenant role and their role on the target project. Middleware computes
  this per request (ADR-0007).
- Platform-admin bypasses the hierarchy entirely via the separate
  `/api/admin/*` surface.

## How it maps to Proxmox

- Each project owns a pool named `pc-<tenant-slug>-<project-slug>` (slugs
  lowercased, `[a-z0-9-]`, truncated, collision-suffixed). The resolved
  pool id is stored on the project and never recomputed.
- Guests are placed into their project's pool at create time, after an
  ensure-pool-exists step. This makes the Proxmox UI a readable mirror of
  Proxcloud's tenancy.
- Node placement is **tenant-chosen** — non-admins pick a node by name
  with no capacity detail. Quotas (per-tenant and per-project) bound how
  much a project may consume regardless of the node chosen (ADR-0009).

## Non-negotiable rules (see CLAUDE.md)

1. Every tenant-scoped query is tenant-filtered — no cross-tenant path.
2. No route ships without a permission-table entry (enforced by a test).
3. No mutation completes without an audit entry (structural + tested).
4. Cross-tenant access to a vmid/project/tenant returns **404, never 403**.
