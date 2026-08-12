# ADR-0007: Tenancy model & authorization

Date: 2026-08-12 · Status: accepted

## Context

v1's `RequireSession` injects nothing into the request context, and every
handler takes a client-supplied VMID straight into a Proxmox call with no
ownership check. Multi-tenancy needs a scoping model, a place to carry the
active tenant on every request, and a structural guarantee that no route
can read or mutate across tenant boundaries.

## Decision

- **Hierarchy:** Tenant → Project → Resources. Users are **global**
  identities; access is granted via **memberships** at tenant or project
  scope. Roles: **Owner** (full + manage members/quotas/projects),
  **Contributor** (CRUD resources), **Reader** (view incl. metrics). A
  tenant role **inherits** to every project; a project role can only
  **add** privilege, never subtract. A separate **platform-admin** flag
  governs cross-tenant and cluster-infra operations.
- **Tenant carried in the URL path:** `/api/tenants/{tenantId}/…`. This is
  self-documenting, structurally present on every scoped request (a route
  cannot forget it), and lets authorization be expressed cleanly per
  route pattern. Rejected: a tenant header — invisible in logs/routing,
  easy to omit, and it would force every handler to re-derive scope.
- **Middleware chain** replacing the single `RequireSession`:
  `authenticate` (cookie→session→user) → `resolveTenant` (path param
  validated against membership; **404 if not a member**) →
  `loadEffectiveRole` (max of tenant role + project role for the target)
  → `enforce` (permission-table lookup). Platform-admin routes live under
  a separate `/api/admin/*` group and bypass tenant scoping.
- **Infra visibility = platform-admin.** Full Nodes/Storage/cluster-
  capacity views move to `/api/admin/*`. Tenant users get a tenant-scoped
  dashboard plus the **minimal catalog** (nextid, bridges, storages for
  the placeable nodes) needed to create a guest. **Node placement stays
  tenant-chosen** — names only, no capacity detail for non-admins.
- **Permission-table registry** (`internal/authz/permissions.go`) keyed on
  the matched chi `RoutePattern()` → `minRole | platformAdmin`; the guest
  `{action}` wildcard maps per action. A `permissions_test.go` walks the
  mounted router (`chi.Walk`) and **fails if any route lacks an entry**.
- **IDOR rule: cross-tenant → 404, never 403** (no existence leak). A
  single **ownership middleware** runs before every handler carrying
  `{vmid}`/`{projectId}`: it looks up `resource_ownership`, requires
  `tenant_id == activeTenant` and that the effective project role covers
  it, else 404. It also guards the **clone-source `CloneVMID`** in create/
  deploy — the template must be owned in the same tenant.

## Consequences

- Authorization is declarative and testable; adding a route without a
  permission entry fails CI, and no handler can skip the ownership check.
- The path scheme coexists with the frontend's query-key invalidation
  constraint: `tenantId` lives **inside the `ResourceFilters` object, not
  the leading key segment**, so existing `["resources"]`/`["tasks"]`/
  `["guest",…]` prefix invalidations keep matching; switching tenants
  invalidates all scoped queries.
- Effective-role resolution is a per-request join (membership rows are
  indexed and few per user); cheap enough to compute inline.
