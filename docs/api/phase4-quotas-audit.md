# API contract — Phase 4 (quotas + audit + activity log)

Extends `docs/api/phase3-tenancy.md`. Shared types remain sourced from
`backend/api/types` (tygo-regenerated). Error envelope unchanged:
`{ "error": { code, message, pveMessage? } }`. `{tenantId}`/`{projectId}` are
UUIDs. Ownership/tenant-boundary misses → **404**; role denials within a joined
tenant → **403**; over-quota creates → **409** (see below). Auth column legend as
Phase 3: `Reader`/`Contributor`/`Owner` (effective role) · `Admin`
(platform-admin).

---

## Shared types (`backend/api/types/quota.go`, `activity.go`)

```go
// Nil pointer = unlimited on that dimension.
type QuotaLimits struct {
	MaxVCPU   *int   `json:"maxVcpu"`
	MaxRAMMB  *int64 `json:"maxRamMb"`
	MaxDiskGB *int64 `json:"maxDiskGb"`
	MaxCount  *int   `json:"maxCount"`
}

// Live usage for a scope. Sums PROVISIONED allocations (ADR-0012 §4):
// active guests from ClusterResources (MaxCPU / MaxMem / MaxDisk), pending
// reservations from resource_ownership.reserved_*.
type QuotaUsage struct {
	VCPU   int   `json:"vcpu"`
	RAMMB  int64 `json:"ramMb"`
	DiskGB int64 `json:"diskGb"`
	Count  int   `json:"count"`
}

type QuotaWithUsage struct {
	ScopeType string      `json:"scopeType"` // "tenant" | "project"
	ScopeID   string      `json:"scopeId"`
	Limits    QuotaLimits `json:"limits"`
	Usage     QuotaUsage  `json:"usage"`
	// Remaining[dim] present only where the matching limit is non-null.
	Remaining QuotaUsage  `json:"remaining"`
}

type SetQuotaRequest struct { // PUT body; each nil field clears that limit (→ unlimited)
	MaxVCPU   *int   `json:"maxVcpu"`
	MaxRAMMB  *int64 `json:"maxRamMb"`
	MaxDiskGB *int64 `json:"maxDiskGb"`
	MaxCount  *int   `json:"maxCount"`
}

// Project GET embeds the tenant rollup so the wizard can bind on the tighter of
// the two (min(projectRemaining, tenantRemaining)) in one round-trip.
type ProjectQuotaResponse struct {
	Project QuotaWithUsage `json:"project"`
	Tenant  QuotaWithUsage `json:"tenant"`
}

// One row in the merged activity timeline (audit intent + PVE task feed).
type ActivityEntry struct {
	ID          string          `json:"id"`          // audit uuid, or "task:"+upid
	Source      string          `json:"source"`      // "audit" | "task"
	TS          time.Time       `json:"ts"`
	Actor       string          `json:"actor"`       // display name/email; "system" for reconciler
	Action      string          `json:"action"`      // "guest.create", "project.rename", PVE label…
	TargetType  string          `json:"targetType"`  // "guest"|"project"|"tenant"|"member"|"quota"|""
	TargetID    string          `json:"targetId"`    // vmid / projectId / ""
	Outcome     string          `json:"outcome"`     // audit: pending|success|denied|error · task: running|succeeded|failed
	ProjectID   string          `json:"projectId"`
	ProjectName string          `json:"projectName"`
	UPID        string          `json:"upid,omitempty"`   // task rows only
	Detail      json.RawMessage `json:"detail,omitempty"` // audit detail passthrough
}

type ActivityPage struct {
	Entries    []ActivityEntry `json:"entries"`
	NextBefore *time.Time      `json:"nextBefore"` // pass back as ?before= ; null when no older audit rows
}
```

---

## Quotas

### GET /api/tenants/{tenantId}/quota — `Reader`
Tenant limits + live usage for the dashboard usage bars. Response `QuotaWithUsage`
(`scopeType:"tenant"`). No stored quota row ⇒ all limits null (unlimited); usage
is still computed.

### GET /api/tenants/{tenantId}/projects/{projectId}/quota — `Reader`
Project limits+usage **and** the parent tenant rollup. Response
`ProjectQuotaResponse`. Drives per-project bars and the wizard's inline remaining
quota. 404 if the project is not in the tenant.

### PUT /api/tenants/{tenantId}/projects/{projectId}/quota — `Owner`
Body `SetQuotaRequest`. An Owner subdivides tenant capacity across projects.
**Validation:** each non-null project limit must be ≤ the corresponding tenant
limit (per-dimension); `400 { code:"invalid_request" }` otherwise. Sum-of-projects
≤ tenant is **not** enforced (the tenant check at create time is the backstop —
ADR-0012 / flagged). Response **200** `QuotaWithUsage` (project scope). Audited
as `project.quota.update`.

### GET /api/admin/tenants/{tenantId}/quota — `Admin`
Same body as the tenant GET; for the platform-admin quota screen.

### PUT /api/admin/tenants/{tenantId}/quota — `Admin`
Body `SetQuotaRequest`. **Tenant quotas are platform-admin-only** — a tenant
Owner may not raise their own cap. Response **200** `QuotaWithUsage`. Audited as
`tenant.quota.update` (tenant_id set; actor = admin).

### Over-quota create — `POST /api/tenants/{tenantId}/guests`
Unchanged request shape (Phase 3). The reservation now runs a concurrency-safe
quota check **before any Proxmox call** (ADR-0009/0012). If the requested guest
would exceed the project **or** tenant limit on any dimension, the endpoint
returns **409** *without touching PVE*:
```jsonc
{ "error": {
  "code": "quota_exceeded",
  "message": "Creating this guest would exceed the project vCPU quota (using 6 of 8, requested 4)."
} }
```
The message names the tightest violated dimension and scope. The frontend keys on
`code === "quota_exceeded"`. A duplicate VMID is still `409 { code:"conflict" }`
(distinct code). Non-quota validation still returns `400`.

---

## Activity log

### GET /api/tenants/{tenantId}/activity — `Reader`
Query: `limit` (default 50, max 200), `before` (RFC3339; keyset cursor on the
audit spine), `source` (`audit|task`, optional), `projectId` (optional),
`outcome` (optional). Response `ActivityPage`.

Merge semantics (ADR-0010): the **audit_log is the paginated spine**
(`WHERE tenant_id=$t [AND ts < before] ORDER BY ts DESC LIMIT limit`); the **PVE
task feed is a live overlay** — `ClusterTasks` filtered to the tenant's owned
VMIDs (reusing `tenantOwnedVMIDs` + `taskSummary`), included for `ts` within the
page's time window `[oldestAuditTs, before)`. Both are normalized to
`ActivityEntry`, merged, sorted `ts` DESC, truncated to `limit`. `NextBefore` =
`ts` of the oldest audit row when more remain, else null. Task history that PVE
has rotated out simply ages off the overlay (honest — no fabricated backfill).

---

## Permission-registry additions (`internal/authz/permissions.go`)

Append to the exact set the completeness test asserts:

```
GET  /api/tenants/{tenantId}/quota                                 Reader
GET  /api/tenants/{tenantId}/projects/{projectId}/quota            Reader
PUT  /api/tenants/{tenantId}/projects/{projectId}/quota            Owner
GET  /api/tenants/{tenantId}/activity                              Reader
GET  /api/admin/tenants/{tenantId}/quota                           PlatformAdmin
PUT  /api/admin/tenants/{tenantId}/quota                           PlatformAdmin
```

`PUT …/projects/{projectId}/quota` is a mutation on the tenant subtree ⇒ it flows
through `AuditOnMutation` and MUST have an entry in the audit action-map (below),
or the audit-completeness test fails.

---

## Audit action-map (`internal/authz/audit_actions.go`)

Static `(method, routePattern) → action` map, exhaustive over every mutating
(non-GET) tenant route pattern (asserted by `audit_completeness_test.go`). `{action}`
and other volatile segments are read from `chi.URLParam` at request time.

```
POST   …/guests                                            guest.create
DELETE …/guests/{node}/{type}/{vmid}                       guest.delete
POST   …/guests/{node}/{type}/{vmid}/{action}              guest.{action}      # start|stop|reboot|…
PATCH  …/guests/{node}/{type}/{vmid}/config                guest.config.update
POST   …/guests/{node}/{type}/{vmid}/resize                guest.disk.resize
POST   …/guests/{node}/{type}/{vmid}/snapshots             guest.snapshot.create
POST   …/guests/{node}/{type}/{vmid}/snapshots/{name}/rollback  guest.snapshot.rollback
DELETE …/guests/{node}/{type}/{vmid}/snapshots/{name}      guest.snapshot.delete
PUT    …/guests/{node}/{type}/{vmid}/firewall/options      guest.firewall.update
POST   …/guests/{node}/{type}/{vmid}/console               guest.console.open
POST   …/projects                                          project.create
PATCH  …/projects/{projectId}                              project.rename
DELETE …/projects/{projectId}                              project.delete
PUT    …/projects/{projectId}/quota                        project.quota.update
```

Audit row fields (ADR-0010/0012): `actor_user_id`=identity.UserID,
`tenant_id`=ActiveTenantID, `project_id`=ResolvedProjectID (nil at tenant level),
`action`=map lookup, `target_type`/`target_id` from path params
(`{vmid}`→"guest", `{projectId}`→"project"; null for creates whose id is not in
the path), `outcome` pending→(2xx `success` | 4xx `denied` | 5xx `error`),
`ip`=RealIP, `detail` jsonb = `{status, upid?, action?}` plus any handler
annotations. Optional non-load-bearing enrichment: handlers may call
`audit.Annotate(ctx, k, v)` (e.g. guest.create adds `vmid`,`name`) — the
structural one-row guarantee never depends on it.
