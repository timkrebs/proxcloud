# Phase 3 — Tenancy core: build spec

Branch: `feat/multi-tenancy`. Builds on Phase 1 (Postgres/store/migrations/permission-table)
and Phase 2 (local auth, DB sessions, Argon2id, bootstrap, env-admin cutover), both green.
Implements ADR-0007 (authz), ADR-0008 (pools), and the ownership half of ADR-0010.
Quotas (ADR-0009) and the audit *write* (ADR-0010) are **seamed but not built** here — Phase 4.

This is a build spec for backend-engineer and frontend-engineer. The wire contract lives in
`docs/api/phase3-tenancy.md`. Do not deviate from the endpoint shapes there.

---

## 0. Decisions that need Tim (flagged)

1. **SSE re-scoping depth.** `GET /api/events` today streams cluster-wide node metrics + all
   task events — a cross-tenant existence leak once tenants exist. Phase 3 **required minimum:**
   task-event payloads filtered per-connection to the subscriber's active-tenant owned VMIDs;
   node-metrics frames emitted only to platform-admin subscribers. Full metrics rework
   (admin-only stream vs per-guest RRD for tenant dashboards) is deferred. **Confirm the minimum
   is acceptable for the phase; if yes this becomes ADR-0011.**
2. **Admin tenant-create side effect.** `POST /api/admin/tenants` should also create a `default`
   project + its pool so the tenant is immediately usable. **Confirm.** (Spec assumes yes.)
3. **`CreateGuestRequest.Pool` → `ProjectId`.** The wizard stops sending `pool`; the backend
   derives the pool from the mandatory `projectId`. This is a breaking change to the internal
   create contract (frontend co-changes in the same phase). **Confirm.**
4. **Platform-admin on the tenant surface.** A platform-admin passes `resolveTenant` for *any*
   tenant, treated as effective Owner (support/impersonation, and so admins never 404 their own
   default tenant). Cross-tenant *management* still goes through `/api/admin/*`. **Confirm** (vs.
   strictly forcing admins onto `/api/admin/*`).

---

## 1. Authz middleware chain

Replaces the single `d.Auth.Authenticate` on the protected group. Order, per group:

```
authenticate → resolveTenant → resolveScope → enforce → auditOnMutation(stub) → handler
```

### Extended identity (no import cycle)
`auth.Identity` (in `internal/auth/session.go`) gains four fields, mutated in place by the
middleware (the `*Identity` is a per-request pointer already in context):

```go
type Identity struct {
    UserID, Email string
    IsPlatformAdmin bool
    SessionID string
    // --- set by the authz chain (Phase 3) ---
    ActiveTenantID    string // resolveTenant
    TenantRole        string // resolveTenant: max tenant-scope membership role ("" if none)
    ResolvedProjectID string // resolveScope: from {projectId} or {vmid}→ownership ("" tenant-level)
    EffectiveRole     string // resolveScope: max(TenantRole, projectRole) for this request
}
```

Roles are plain strings in `auth` (`"owner"|"contributor"|"reader"|""`). The ordering/compare
type lives in `authz` so `auth` never imports `authz`. The whole chain lives in a new
`internal/authz/middleware.go` (imports `auth` + `store`); `authz` stays free of `auth` for the
`Rule`/`Role` value types (new `internal/authz/roles.go`).

- `authz.Role` constants + `RoleAtLeast(have, need Role) bool` (owner>contributor>reader).
- `RuleMinRole(Rule) Role` maps the Phase-1 `Rule` enum to a minimum role.

### Middleware, exactly

- **`ResolveTenant(store)`** — reads `chi.URLParam("tenantId")`. If platform-admin → set
  `ActiveTenantID`, `TenantRole="owner"`, pass. Else `store.ListMembershipsByUser` (or a targeted
  `GetMembershipRoleForTenant`) to compute the caller's max **tenant-scope** role *and* whether
  they hold **any** project membership inside that tenant. Not a member at either level → **404**
  (no existence leak). Set `ActiveTenantID`, `TenantRole` ("" allowed when project-only member).
- **`ResolveScope(store)`** — resolves the request's project + effective role:
  - `{projectId}` present → `store.GetProjectByID`; if `project.TenantID != ActiveTenantID` →
    **404**. `EffectiveRole = max(TenantRole, projectRole(projectId))`, `ResolvedProjectID=projectId`.
  - `{vmid}` present → call the **ownership helper** (§3). 404 on mismatch. Sets
    `ResolvedProjectID` from the ownership row; `EffectiveRole = max(TenantRole, projectRole)`.
  - neither → tenant-level route: `EffectiveRole = TenantRole`, `ResolvedProjectID=""`.
- **`Enforce()`** — `authz.Lookup(method, chi.RouteContext().RoutePattern())`; the pattern now
  includes the `/api/tenants/{tenantId}` prefix. Compare `EffectiveRole ≥ RuleMinRole`. Fail →
  **403** (role denial within a tenant the caller provably belongs to — not an existence leak).
  Unregistered route → 500 + loud log (the completeness test prevents this shipping).
- **`AuditOnMutation(store)`** — Phase-4 stub. Present on the mutating (non-GET) group so the
  choke-point exists now; Phase 3 implementation is a pass-through that reads
  `ActiveTenantID`/`ResolvedProjectID`/actor from context and **logs** at debug. Do not write
  `audit_log` yet, but wire the middleware so Phase 4 only fills the body.
- **`RequirePlatformAdmin()`** — for `/api/admin/*`: `IsPlatformAdmin` else 403.

### Router wiring (`internal/httpserver/router.go`, the group at lines 77–92)

Split the single authenticated group into three sub-surfaces. `Deps` gains `Authz *authz.Middleware`,
`Admin func(chi.Router)`, `Tenant func(chi.Router)`; `Protected` is retired.

```
r.Group(authenticated):                       // r.Use(d.Auth.Authenticate)
  account routes: /auth/logout, /auth/me, PATCH /auth/active-tenant, /auth/password,
                  /auth/sessions[...], /events, /notifications[...], /pricing
  r.Route("/admin"):    r.Use(RequirePlatformAdmin);  d.Admin(r)
  r.Route("/tenants/{tenantId}"):
      r.Use(ResolveTenant); r.Use(ResolveScope); r.Use(Enforce)
      r.Group(mutating): r.Use(AuditOnMutation)      // non-GET subtree
      d.Tenant(r)
```

`handlers.Deps.Mount` is refactored into `MountAccount`, `MountAdmin`, `MountTenant`. The authz
completeness test (`permissions_test.go`) builds the **real** router, so it will exercise the new
patterns automatically — the registry in §2 must match exactly or CI fails.

---

## 2. URL scheme + route moves + permission registry

`/api/tenants/{tenantId}/…` = tenant-scoped; `/api/admin/…` = platform-admin; a small
tenant-agnostic account/stream surface stays flat.

| Old route | New route | Rule |
|---|---|---|
| `GET /api/resources` | `GET /api/tenants/{tenantId}/resources` (scoped) **+** `GET /api/admin/resources` (all) | Reader / PlatformAdmin |
| `GET /api/cluster`, `/cluster/nextid` | `GET /api/admin/cluster`, `/api/admin/cluster/nextid` **+** `GET …/catalog/nextid` | PlatformAdmin / Contributor |
| `GET /api/nodes…` (all) | `GET /api/admin/nodes…` (full) **+** `…/catalog/nodes…` (minimal) | PlatformAdmin / Contributor |
| `GET /api/pools`, `/api/storage` | `GET /api/admin/pools`, `/api/admin/storage` | PlatformAdmin |
| `…/api/guests/*` | `…/api/tenants/{tenantId}/guests/*` | Reader/Contributor + ownership |
| `POST /api/guests` | `POST /api/tenants/{tenantId}/guests` | Contributor |
| `GET /api/tasks*`, `/deployments/{id}` | `…/tenants/{tenantId}/tasks*`, `…/deployments/{id}` **+** `/api/admin/tasks*` | Reader / PlatformAdmin |
| `GET /api/notifications`, `/pricing` | unchanged (per-user / static) | Authenticated |

The **catalog** projection (`…/catalog/*`) reuses the existing PVE calls but strips capacity/sensitive
fields: `nodes` returns names only; `storages` returns id + content types + free/total **omitted**
for non-admins (id + content only). Node placement stays tenant-chosen by name (ADR-0007 §4).

**Full permission registry** (replace `buildRegistry()` in `internal/authz/permissions.go`; this
is the exact set the completeness test asserts):

```
# public
GET    /api/health                                  Public
GET    /api/auth/bootstrap-status                   Public
POST   /api/auth/bootstrap                          Public
POST   /api/auth/login                              Public
GET    /api/console/ws/{sessionId}                  Public
# authenticated / tenant-agnostic
POST   /api/auth/logout                             Authenticated
GET    /api/auth/me                                 Authenticated
PATCH  /api/auth/active-tenant                      Authenticated   # NEW
POST   /api/auth/password                           Authenticated
GET    /api/auth/sessions                           Authenticated
DELETE /api/auth/sessions/{id}                      Authenticated
GET    /api/events                                  Authenticated
GET    /api/notifications                           Authenticated
POST   /api/notifications/read                      Authenticated
GET    /api/pricing                                 Authenticated
# platform-admin
GET    /api/admin/tenants                           PlatformAdmin
POST   /api/admin/tenants                           PlatformAdmin
GET    /api/admin/cluster                           PlatformAdmin
GET    /api/admin/cluster/nextid                    PlatformAdmin
GET    /api/admin/nodes                             PlatformAdmin
GET    /api/admin/nodes/{node}                      PlatformAdmin
GET    /api/admin/nodes/{node}/metrics              PlatformAdmin
GET    /api/admin/nodes/{node}/bridges              PlatformAdmin
GET    /api/admin/nodes/{node}/storages             PlatformAdmin
GET    /api/admin/nodes/{node}/storages/{storage}/content   PlatformAdmin
GET    /api/admin/resources                         PlatformAdmin
GET    /api/admin/pools                             PlatformAdmin
GET    /api/admin/storage                           PlatformAdmin
GET    /api/admin/tasks                             PlatformAdmin
GET    /api/admin/tasks/{upid}                      PlatformAdmin
GET    /api/admin/tasks/{upid}/log                  PlatformAdmin
# tenant-scoped
GET    /api/tenants/{tenantId}/summary              Reader
GET    /api/tenants/{tenantId}/projects             Reader
POST   /api/tenants/{tenantId}/projects             Owner
GET    /api/tenants/{tenantId}/projects/{projectId} Reader
PATCH  /api/tenants/{tenantId}/projects/{projectId} Owner
DELETE /api/tenants/{tenantId}/projects/{projectId} Owner
GET    /api/tenants/{tenantId}/members              Owner
GET    /api/tenants/{tenantId}/resources            Reader
GET    /api/tenants/{tenantId}/catalog/nextid       Contributor
GET    /api/tenants/{tenantId}/catalog/nodes        Contributor
GET    /api/tenants/{tenantId}/catalog/nodes/{node}/bridges   Contributor
GET    /api/tenants/{tenantId}/catalog/nodes/{node}/storages  Contributor
GET    /api/tenants/{tenantId}/catalog/nodes/{node}/storages/{storage}/content  Contributor
POST   /api/tenants/{tenantId}/guests               Contributor
GET    /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}                    Reader
PATCH  /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/config             Contributor
GET    /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/metrics            Reader
GET    /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/interfaces         Reader
POST   /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/resize             Contributor
GET    /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/snapshots          Reader
POST   /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/snapshots          Contributor
POST   /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/snapshots/{name}/rollback  Contributor
DELETE /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/snapshots/{name}   Contributor
GET    /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/firewall           Reader
PUT    /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/firewall/options   Contributor
GET    /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/acl                Reader
POST   /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/{action}           Contributor
DELETE /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}                    Contributor
POST   /api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/console            Contributor
GET    /api/tenants/{tenantId}/deployments/{id}     Reader
GET    /api/tenants/{tenantId}/tasks                Reader
GET    /api/tenants/{tenantId}/tasks/{upid}         Reader
GET    /api/tenants/{tenantId}/tasks/{upid}/log     Reader
```

Console is Contributor (an interactive console is full control, not a read). The `{action}`
wildcard is Contributor for every action; the handler still rejects unknown actions with 404.

---

## 3. IDOR prevention

Single ownership helper, one code path for every `{vmid}`:

```go
// internal/authz/ownership.go
func ResolveOwnership(ctx, s store.OwnershipStore, vmid int, tenantID string) (*store.ResourceOwnership, error)
// returns ErrNotOwned (→ 404) unless a row exists with tenant_id == tenantID AND status in (active,pending)
```

Insertion points:
- **`ResolveScope` middleware** calls it for every route carrying `{vmid}` — covers all of
  `guest_actions.go`, `guest_detail.go`, `snapshots_firewall.go`, `console.go`. Handlers read
  `ResolvedProjectID` from context; no per-handler check.
- **Create / clone source** — `handlers.CreateGuest`: before `d.Deploy.Submit`, when
  `req.Source.Mode == "clone"`, call `ResolveOwnership(req.Source.CloneVMID, ActiveTenantID)`.
  404 if the template is not owned in the same tenant. Pass the result into `deploy.CreateContext`.
- **Routes whose path lacks `{vmid}`** but reference a guest — `tasks/{upid}`, `tasks/{upid}/log`,
  `deployments/{id}`: the handler extracts the VMID (from `UPID.ID`/the parsed UPID for tasks;
  from the deployment record for deployments) and calls `ResolveOwnership` explicitly, else 404.

`404 never 403` for every ownership miss. Tests: user A gets **404** on user B's VMID across the
full guest route matrix + the clone-source path + tasks/{upid} + deployments/{id}.

---

## 4. Store additions (finish the sub-interface decomposition)

New sub-interfaces embedded in `store.Store`; pgx impl in `postgres.go` + test double. All
parameterized, all tenant/project filters **in the SQL**.

```go
type TenantStore interface {
    CreateTenant(ctx, CreateTenantParams) (*Tenant, error)   // name, slug
    GetTenantByID(ctx, id string) (*Tenant, error)
    ListTenantsForUser(ctx, userID string) ([]TenantWithRole, error) // tenant + project memberships
}
type ProjectStore interface {
    CreateProject(ctx, CreateProjectParams) (*Project, error) // tenantID, name, slug, poolID
    GetProjectByID(ctx, id string) (*Project, error)
    RenameProject(ctx, id, name string) (*Project, error)
    DeleteProject(ctx, id string) error                       // caller checks emptiness first
    CountActiveOwnershipByProject(ctx, projectID string) (int, error)
}
type OwnershipStore interface {
    GetOwnershipByVMID(ctx, vmid int) (*ResourceOwnership, error)
    CreateOwnership(ctx, CreateOwnershipParams) (*ResourceOwnership, error) // status pending|active
    FinalizeOwnership(ctx, id string, upid string) error                    // pending→active
    ReleaseOwnership(ctx, id string) error                                  // failed create → free
    TombstoneOwnership(ctx, id string) error                                // reconciler (Phase 4)
    ListOwnershipByTenant(ctx, tenantID string) ([]ResourceOwnership, error)
    ListOwnershipByProject(ctx, projectID string) ([]ResourceOwnership, error)
    ListActiveVMIDs(ctx) (map[int]bool, error)                              // backfill/reconciler
}
// MembershipStore adds:
    ListMembershipsByScope(ctx, scopeType, scopeID string) ([]Membership, error)
    GetEffectiveRoles(ctx, userID, tenantID string) (tenantRole string, projectRoles map[string]string, error error)
// UserStore adds:
    ListUsersByIDs(ctx, ids []string) (map[string]User, error) // creator column, no N+1
```

`GetEffectiveRoles` is the single query behind `ResolveTenant`/`ResolveScope` (one indexed
membership scan per request; ADR-0007 says cheap). **Reservation seam (Phase 4):** `CreateOwnership`
already accepts `Status`; Phase 4 wraps the insert in `WithTx`+`AdvisoryLock(hash(projectID))`+quota
re-check. Do **not** build the lock/quota here. Phase 3 uses `pending`→`Finalize`/`Release`
directly (honest task states) without the lock.

---

## 5. Ownership backfill (idempotent, pre-serve)

New package `internal/bootstrap` with `BackfillOwnership(ctx, s store.Store, pve proxmox.Client, log)`
and the shared `ClaimIntoProject(ctx, s, pve, row RawResource, tenantID, projectID string, actor *string)`
the reconciler will reuse (ADR-0010).

Steps (all PVE calls best-effort — log + continue, **never** fail boot):
1. `EnsureProjectPool` for `pc-default-default` (CreatePool; treat "already exists" as success).
2. Load default tenant (`GetTenantBySlug("default")`) + default project (`GetProjectByPoolID("pc-default-default")`).
3. `ListActiveVMIDs()` once; `pve.ClusterResources()`; for each qemu/lxc row **not** already owned:
   `CreateOwnership{tenant, project, vmid, guestType, node, status:"active", createdBy:nil}` then
   best-effort `AddPoolMembers(pool, [vmid])`.

**Ordering (critical, call out in the runbook):** in `main.go` the sequence is
`RunMigrations → SeedEnvAdmin → construct pve → BackfillOwnership → serve`. Backfill runs
**synchronously before `ListenAndServe`** so scoping enforcement (always on once the router is up)
never 404s a pre-existing guest. Insert the call after `pve` is built (~main.go:92), before the
`http.Server` block. Transient PVE failure logs; the next boot (or the Phase-4 reconciler) re-attempts.

---

## 6. Pools at create (ADR-0008)

New `proxmox.Client` methods + `proxmoxtest` mock `On*` fields + gopve impl (`internal/proxmox/pools.go`,
raw `/pools`):

```go
CreatePool(ctx, poolID, comment string) error   // POST /pools ; "already exists" == success
DeletePool(ctx, poolID string) error            // DELETE /pools/{poolid}
AddPoolMembers(ctx, poolID string, vmids []int) error  // PUT /pools/{poolid}
```

Create flow (`handlers.CreateGuest` → `deploy.Engine`): the **handler** resolves `projectId` →
project, calls `EnsureProjectPool(project)`, sets the pool on the request, and passes a
`deploy.CreateContext{TenantID, ProjectID, PoolID, ActorUserID, CloneSourceOK bool}` into `Submit`.
The engine keeps building `p["pool"] = ctx.PoolID` (existing passthrough) — no store dependency in
`deploy`. `deploy.Engine.run` replaces `context.Background()` (engine.go:97) with a ctx carrying the
`CreateContext`. On the create task **succeeding**, finalize the pending ownership row
(`FinalizeOwnership`); on failure/timeout `ReleaseOwnership`. proxmox-specialist verifies exact
`/pools` bodies + `Pool.Allocate` privilege; the method **signatures above are fixed** so their
findings drop into `pools.go` only.

---

## 7. Scoped ListResources

New `GET /api/tenants/{tenantId}/resources` handler (fork of `resources.go`):
`ListOwnershipByTenant(tenantID)` (tenant filter **in SQL**) → build `map[vmid]ownership`; call
`pve.ClusterResources()`; keep only rows whose vmid is in the map (+ optional `?projectId`, `?type`,
`?node`, `?search`); enrich each `GuestSummary` with project + creator. `GuestSummary`
(`api/types/resources.go`) gains:

```go
ProjectID   string `json:"projectId"`
ProjectName string `json:"projectName"`
CreatedBy   string `json:"createdBy"` // display name or email; "" for backfilled/unknown
```

Creator resolved via `ListUsersByIDs` over the ownership set (no N+1). Admin `GET /api/admin/resources`
returns the unfiltered list with an added `"unassigned"` marker for VMIDs lacking an ownership row
(feeds the Phase-6 unassigned view). The old flat `ListResources` is deleted (registry stale-check
enforces removal).

---

## 8. Frontend

- **`uiStore.ts`** — add `activeTenantId: string | null` + `setActiveTenant`, wrapped in zustand
  `persist` (localStorage key `proxcloud.activeTenant`). Hydrate on app load from `/api/auth/me`:
  prefer the persisted id **iff** still in `me.tenants`; else `me.activeTenantId`; else
  `me.tenants[0]`.
- **`/api/auth/me`** — extended to return `tenants: [{id,name,slug,role}]` + `activeTenantId`.
  Tenant switch: `PATCH /api/auth/active-tenant {tenantId}` (updates `sessions.active_tenant_id`;
  404 if not a member) → set store → `qc.invalidateQueries()` for all scoped keys →
  **reconnect the EventSource** (add `activeTenantId` to the `useEvents` effect deps so the server
  re-derives the ownership filter from the session).
- **`queryKeys.ts`** — `ResourceFilters` gains `tenantId?` + `projectId?` **inside the object**
  (`qk.resources(f) = ["resources", f]` unchanged leading segment, so `sse.ts`/`mutations.ts`
  `["resources"]`/`["tasks"]`/`["guest",…]` prefix invalidations keep matching). Add
  `qk.projects(tenantId)`, `qk.project(tenantId, projectId)`, `qk.members(tenantId)`,
  `qk.tenantSummary(tenantId)`. `["guest", node, type, vmid]` stays tenant-less (VMIDs are
  cluster-unique).
- **`client.ts`/hooks** — all guest/resource/task/deployment/catalog calls prepend
  `/api/tenants/${activeTenantId}`. `mutations.ts` guest URLs change accordingly. `apiFetch`'s
  401→/signin behavior unchanged.
- **`ClusterPane.tsx`** (opened by the TopBar user-chip → `setPane("tenant")`) becomes the real
  Azure directory switcher: **TENANTS** section (from `me.tenants`, radio-select → `setActiveTenant`
  + PATCH) then **PROJECTS** in the active tenant (from `qk.projects`, radio → resources
  `projectId` filter). Keep the footer Done + Sign out.
- **Resources table** — add **Project** + **Creator** columns and a Project filter (dropdown from
  `qk.projects`). Empty/loading/error states per DoD.
- **Projects CRUD** — create (name → slug preview, calls `POST …/projects`), rename (`PATCH`),
  delete (typed-name confirm; button disabled unless project is empty; backend re-checks emptiness
  and 409s otherwise). Owner-only UI (hide for Reader/Contributor).
- **Wizard `BasicsTab`** (`tabs.tsx:96–109`) — replace the optional Pool select with a **mandatory
  Project selector** (from `qk.projects`); validate in `wizardStore.validateWizard` tab 0;
  `toCreateRequest` sends `projectId` (drops `pool`).

---

## 9. New/changed files (index)

Backend — new: `internal/authz/{middleware.go,roles.go,ownership.go}`,
`internal/handlers/{tenants.go,projects.go,members.go,catalog.go,admin.go}`,
`internal/bootstrap/backfill.go`, `internal/proxmox/pools.go`,
`backend/api/types/tenancy.go`. Changed: `internal/authz/permissions.go` (registry),
`internal/httpserver/router.go` (3-surface split), `internal/handlers/handlers.go`
(Deps + MountAccount/MountAdmin/MountTenant), `internal/handlers/{resources.go,create.go,guest_actions.go,tasks.go}`,
`internal/store/{store.go,postgres.go}` (+ test double), `internal/auth/session.go` (Identity fields),
`internal/deploy/engine.go` (CreateContext), `backend/api/types/{auth.go,resources.go,create.go}`,
`internal/proxmox/{client.go,proxmoxtest/mock.go}`, `backend/cmd/proxcloud/main.go` (backfill wiring).

Frontend — new: tenant switcher pane content, projects screens, project selector. Changed:
`lib/stores/uiStore.ts`, `lib/api/{queryKeys.ts,client.ts,mutations.ts}`, `lib/sse.ts`,
`components/chrome/ClusterPane.tsx`, resources table, wizard `tabs.tsx` + `wizardStore`.

Endpoint request/response shapes are in `docs/api/phase3-tenancy.md`.

---

## 10. Sequencing + acceptance

**Within Phase 3, in order:**
1. **Schema/store/proxmox** — sub-interfaces + pgx impl + test double; pool methods + mock; type
   additions. No behavior change; `go test ./...` green.
2. **Backfill** — `internal/bootstrap` + `main.go` wiring (pre-serve). Verify every `pve01` guest
   gets a default-tenant/project ownership row + pool membership on a real boot.
3. **Authz chain + router restructure** — roles/middleware/ownership + new registry; completeness
   test green against the new patterns. Enforcement on.
4. **Scoped views + IDOR** — scoped `resources`, guest routes via ownership middleware, create
   (project+pool+pending ownership+clone-source check), tasks/deployments explicit checks.
5. **Frontend** — me/tenants, uiStore, switcher, projects CRUD, resources columns, wizard selector.
6. **Tests + live demo** — Contributor lifecycle in a non-default tenant; second tenant 404s.

**Security acceptance criteria (hand to security-reviewer):**
- Cross-tenant VMID → **404** (not 403) on the full guest route matrix **+** clone-source **+**
  `tasks/{upid}` **+** `deployments/{id}`.
- `resolveTenant` → 404 for a tenant the caller has no membership in.
- Effective role = max(tenant, project); a project role **adds** (tenant Reader + project
  Contributor can mutate that project's guest) and **never subtracts** — table-driven.
- Completeness test green: every mounted route has a registry entry; no stale entries.
- No cross-tenant read path: `resources`/`tasks`/`deployments`/`members` filter by tenant **in SQL**.
- Project delete only when empty (backend-enforced 409); `DeletePool` never orphans a guest.
- Backfill completes before serve; platform never 404s its own pre-existing guests.
- `/api/admin/*` rejects non-admins (403); infra (nodes/storage/pools/full cluster) not reachable
  by tenant users.
- Audit choke-point middleware present on the mutating tenant subtree (structural; write is Phase 4).
- SSE task events carry only the active tenant's owned VMIDs; node-metrics frames admin-only
  (pending Tim's §0.1 confirmation).
