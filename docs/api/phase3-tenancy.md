# API contract — Phase 3 (tenancy core)

The single source of truth for the shared types is `backend/api/types` (tygo-regenerated to
`frontend/src/lib/api/generated/types`). Shapes below mirror those Go structs. Errors always use
the existing envelope `{ "error": { code, message, pveMessage? } }`. `{tenantId}`/`{projectId}` are
UUIDs. Ownership/tenant-boundary misses return **404**; role denials within a joined tenant return
**403**.

Legend for auth column: `Public` · `Auth` (any session) · `Reader`/`Contributor`/`Owner`
(effective role in scope) · `Admin` (platform-admin).

---

## Account & session (tenant-agnostic)

### GET /api/auth/me — `Auth`
Extended. Response `Me`:
```jsonc
{
  "id": "u-uuid", "email": "a@b.c", "displayName": "Ada",
  "isPlatformAdmin": false, "totpEnabled": false,
  "activeTenantId": "t-uuid",                 // from sessions.active_tenant_id; may be null
  "tenants": [                                // TenantMembership[]; from ListTenantsForUser
    { "id": "t-uuid", "name": "Acme", "slug": "acme", "role": "owner" }
  ]
}
```
`role` = the user's highest role anywhere in that tenant (display only; enforcement is per-scope).

### PATCH /api/auth/active-tenant — `Auth`  *(new)*
Request `{ "tenantId": "t-uuid" }`. Sets `sessions.active_tenant_id`. **404** if the caller is not
a member (and not platform-admin). Response **204**. Frontend then invalidates scoped queries and
reconnects the SSE stream.

Unchanged: `POST /api/auth/logout`, `POST /api/auth/password`, `GET /api/auth/sessions`,
`DELETE /api/auth/sessions/{id}`, `GET /api/events`, `GET /api/notifications`,
`POST /api/notifications/read`, `GET /api/pricing`.

---

## Tenant-scoped — `/api/tenants/{tenantId}`

### GET …/summary — `Reader`
`TenantSummary`:
```jsonc
{ "tenant": { "id":"t-uuid","name":"Acme","slug":"acme" },
  "role": "contributor", "projectCount": 3, "resourceCount": 12 }
```

### Projects
`Project`:
```jsonc
{ "id":"p-uuid","tenantId":"t-uuid","name":"Web","slug":"web",
  "poolId":"pc-acme-web","createdAt":"…","updatedAt":"…" }
```
- **GET …/projects** — `Reader` → `Project[]`.
- **POST …/projects** — `Owner`. Request `{ "name":"Web" }`. Slug derived + collision-suffixed
  (ADR-0008); creates the Proxmox pool before returning. Response **201** `Project`. `409` on
  duplicate name in tenant; surfaces the verbatim PVE error in `pveMessage` if pool create fails.
- **GET …/projects/{projectId}** — `Reader` → `Project` (404 if not in tenant).
- **PATCH …/projects/{projectId}** — `Owner`. Request `{ "name":"Web App" }`. Renames only;
  `poolId` never changes (ADR-0008). Response **200** `Project`.
- **DELETE …/projects/{projectId}** — `Owner`. Request `{ "confirmName":"Web" }`. **Only when
  empty** (no active/pending ownership). `409` `{code:"conflict"}` if non-empty; `400` if
  `confirmName` mismatches. Deletes the pool, then the row. Response **204**.

### GET …/members — `Owner`
`Member[]`:
```jsonc
[ { "userId":"u-uuid","email":"a@b.c","displayName":"Ada",
    "scopeType":"tenant","scopeId":"t-uuid","role":"owner" } ]
```
(Invitations / role edits land in Phase 5; read-only here.)

### GET …/resources — `Reader`
Query: `type=qemu|lxc`, `projectId`, `node`, `search`. Returns only the tenant's owned guests.
`GuestSummary[]` — existing fields plus:
```jsonc
{ "…": "…", "projectId":"p-uuid", "projectName":"Web", "createdBy":"Ada" }
```
`createdBy` is "" for backfilled/unknown creators.

### Catalog (wizard placement) — `Contributor`
Minimal, no capacity detail for non-admins.
- **GET …/catalog/nextid** → `{ "vmid": 123 }`.
- **GET …/catalog/nodes** → `[ { "name":"pve01" } ]` (names only).
- **GET …/catalog/nodes/{node}/bridges** → `Bridge[]` (unchanged shape).
- **GET …/catalog/nodes/{node}/storages?content=images|iso|vztmpl** →
  `[ { "storage":"local-lvm", "content":["images"] } ]` (id + content only; free/total omitted).
- **GET …/catalog/nodes/{node}/storages/{storage}/content?content=iso|vztmpl** →
  `StorageContentItem[]` (unchanged; needed to pick an ISO/template).

### Guests
All `{vmid}` routes are ownership-checked by middleware (404 on cross-tenant). GET = `Reader`,
mutations = `Contributor`.

**POST …/guests** — `Contributor`. Request `CreateGuestRequest` (changed):
```jsonc
{ "type":"lxc", "name":"web-01", "node":"pve01", "vmid":0,
  "projectId":"p-uuid",                     // NEW, required; replaces "pool"
  "source": { "mode":"vztmpl", "vztmplVolId":"local:vztmpl/…", "cloneVmid":0, "cloneNode":"", "cloneMode":"" },
  "cores":2, "memoryMb":2048, "diskGb":16, "storage":"local-lvm",
  "bridge":"vmbr0", "vlanTag":0, "firewall":true, "ipConfig":null,
  "cloudInit":null, "tags":[], "startAfterCreate":true }
```
Backend derives the pool from `projectId` (ignores any client `pool`); ensures the pool exists;
inserts a **pending** ownership reservation; on clone (`source.mode=="clone"`) verifies
`source.cloneVmid` is owned in this tenant (else **404**). Response **202**
`{ "deploymentId":"dep_…", "vmid":123 }`. Pending row is finalized on task success, released on
failure.

Other guest routes (paths under `…/guests/{node}/{type}/{vmid}`) keep their v1 request/response
shapes — only the URL prefix and auth change: `/config` (PATCH, Contributor), `/metrics` (GET,
Reader), `/interfaces` (GET, Reader), `/resize` (POST, Contributor), `/snapshots` (GET Reader /
POST Contributor), `/snapshots/{name}/rollback` (POST Contributor), `/snapshots/{name}` (DELETE
Contributor), `/firewall` (GET Reader), `/firewall/options` (PUT Contributor), `/acl` (GET Reader),
`/{action}` (POST Contributor), root DELETE (Contributor, body `{confirmName}`), `/console`
(POST Contributor).

### Deployments & tasks
- **GET …/deployments/{id}** — `Reader`. `Deployment` (unchanged shape). Handler verifies the
  deployment's VMID is tenant-owned → else 404.
- **GET …/tasks** — `Reader`. Query `running`, `vmid`. Filtered to the tenant's VMIDs. `TaskInfo[]`.
- **GET …/tasks/{upid}** — `Reader`. Handler parses the VMID from the UPID and checks ownership
  (404 otherwise). `TaskInfo`.
- **GET …/tasks/{upid}/log** — `Reader`. Same ownership check. `{ lines: TaskLogLine[], total }`.

---

## Platform-admin — `/api/admin` — `Admin`

- **GET /api/admin/tenants** → `Tenant[]`.
- **POST /api/admin/tenants** → `{ "name":"Acme" }` → **201** `Tenant`; also creates a `default`
  project + pool (pending Tim §0.2). `409` on duplicate slug.
- **GET /api/admin/cluster**, **/cluster/nextid** — full `ClusterInfo` / `{vmid}`.
- **GET /api/admin/nodes**, **/nodes/{node}**, **/nodes/{node}/metrics**,
  **/nodes/{node}/bridges**, **/nodes/{node}/storages**,
  **/nodes/{node}/storages/{storage}/content** — full v1 node shapes (capacity included).
- **GET /api/admin/resources** → `GuestSummary[]` cluster-wide; unowned VMIDs carry
  `"projectId":""` + a top-level `"unassigned":true` marker (feeds the Phase-6 view).
- **GET /api/admin/pools** → `Pool[]`. **GET /api/admin/storage** → storage list (v1 shape).
- **GET /api/admin/tasks**, **/tasks/{upid}**, **/tasks/{upid}/log** — cluster-wide, unscoped.

---

## New shared types (`backend/api/types/tenancy.go`)

```go
type Tenant struct { ID, Name, Slug string; CreatedAt, UpdatedAt time.Time }
type TenantMembership struct { ID, Name, Slug, Role string }   // Me.tenants element
type Project struct { ID, TenantID, Name, Slug, PoolID string; CreatedAt, UpdatedAt time.Time }
type Member struct { UserID, Email, DisplayName, ScopeType, ScopeID, Role string }
type TenantSummary struct { Tenant Tenant; Role string; ProjectCount, ResourceCount int }
type CreateProjectRequest struct { Name string }
type RenameProjectRequest struct { Name string }
type DeleteProjectRequest struct { ConfirmName string }
type CreateTenantRequest struct { Name string }
type SetActiveTenantRequest struct { TenantId string }
type CatalogNode struct { Name string }
```
Extends: `Me` (+ `ActiveTenantId string`, `Tenants []TenantMembership`); `GuestSummary`
(+ `ProjectID, ProjectName, CreatedBy string`); `CreateGuestRequest` (+ `ProjectId string`,
`Pool` deprecated/ignored).
