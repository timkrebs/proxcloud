// Package handlers implements the authenticated REST API. Every handler
// depends only on the proxmox.Client interface — no raw HTTP, no library
// types — so table-driven tests run against proxmoxtest.MockClient. Every
// number in a response comes from Proxmox; missing data is an explicit
// error or zero-with-Online=false, never a fabricated value.
package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/catalog"
	"github.com/timkrebs9/proxcloud/backend/internal/console"
	"github.com/timkrebs9/proxcloud/backend/internal/deploy"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/lifecycle"
	"github.com/timkrebs9/proxcloud/backend/internal/mail"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

// Deps carries what every handler needs. Mount attaches all routes.
// Registry and Broker are nil-safe: without them, lifecycle actions still
// work but produce no transitional overlay, notifications, or events.
type Deps struct {
	PVE      proxmox.Client
	Log      *slog.Logger
	Registry *tasks.Registry
	Broker   *events.Broker
	Deploy   *deploy.Engine

	// Store is the Postgres repository — the tenancy system of record.
	Store store.Store

	// Authz is the enforcement chain. Used by MountTenant to wrap the mutating
	// (non-GET) subtree in the audit choke-point. Nil-safe: without it the
	// mutating routes mount directly (test convenience).
	Authz *authz.Middleware

	// Console — nil when PROXMOX_CONSOLE_USER/PASSWORD are unset.
	ConsoleAuth     *console.TicketAuth
	ConsoleSessions *console.Sessions
	ConsoleUser     string

	// Pricing — nil or Enabled=false hides all cost UI.
	Pricing *types.Pricing

	// Invitation delivery (ADR-0013 §2). Mailer sends the accept-link email
	// (SMTP or dev log); nil-safe — CreateInvitation still persists the invite
	// but logs that no mail was sent. FrontendOrigin is the accept-link base
	// (FRONTEND_ORIGIN); when empty the link is unusable and CreateInvitation
	// logs a WARN but does not fail. InvitationTTL bounds how long a minted
	// invite stays valid (defaults to 72h if unset).
	Mailer         mail.Mailer
	FrontendOrigin string
	InvitationTTL  time.Duration

	// AutoShutdown materializes schedules into scheduler jobs (ADR-0019). Nil-safe:
	// without it the schedule CRUD still persists but no jobs are (re)materialized.
	// AutoShutdownEnabled gates materialization on cfg.AutoShutdownActive() so the
	// routes always mount (and persist) but only project jobs when the feature is on.
	AutoShutdown        *lifecycle.AutoShutdown
	AutoShutdownEnabled bool

	// TTL materializes a guest's expiry into scheduler jobs (ADR-0020). Nil-safe:
	// without it the TTL CRUD still persists but no warn/expire jobs are
	// (re)materialized. TTLEnabled gates materialization on cfg.TTLActive() so the
	// routes always mount (and persist) but only project jobs when the feature is on.
	TTL        *lifecycle.TTL
	TTLEnabled bool

	// Service catalog (ADR-0025/0026). Catalog is the loaded, validated set of
	// platform services; nil when CATALOG_ENABLED is off. CatalogEnabled gates the
	// route behavior (the routes always mount so the completeness tests see them;
	// the handlers 404 when the feature is off). SnippetDatastore is the datastore
	// id used in the cicustom snippet reference ("<datastore>:snippets/<file>").
	//
	// CatalogProvisionReady reports whether the SSH/SFTP snippet writer was
	// successfully initialized at boot. The catalog is optional and degrades: when
	// CatalogEnabled is true but the writer could not be built (missing SSH vars, an
	// unreadable key, a bad known_hosts), read-only catalog endpoints still serve but
	// ProvisionService short-circuits to 503 BEFORE any reserve/quota/deploy work —
	// a catalog misconfig must never take the core control plane down (ADR-0025).
	Catalog               *catalog.Catalog
	CatalogEnabled        bool
	CatalogProvisionReady bool
	SnippetDatastore      string

	// DeploymentSetsEnabled gates the multi-guest deployment-set surface
	// (ADR-0029/0030, the K3s cluster action). Like CatalogEnabled the routes always
	// mount (so the completeness tests see them); the handlers 404 when the feature
	// is off. A set rides the catalog + snippet writer, so the handler also requires
	// a loaded Catalog — the flag alone does not enable it.
	DeploymentSetsEnabled bool
}

// MountAccount attaches the tenant-agnostic, per-user account routes (paths
// relative to /api). The /auth/* and /events routes are wired directly from the
// auth.Handler / SSE handler in httpserver.New; these are the handler-owned
// account routes.
func (d *Deps) MountAccount(r chi.Router) {
	r.Get("/notifications", d.ListNotifications)
	r.Post("/notifications/read", d.MarkNotificationsRead)
	r.Get("/pricing", d.GetPricing)
}

// MountAdmin attaches the platform-admin routes (paths relative to
// /api/admin). The caller wraps this group in RequirePlatformAdmin. Full
// cluster/node/storage/pool capacity views live here; tenant users get the
// minimal catalog instead (ADR-0007 §4).
func (d *Deps) MountAdmin(r chi.Router) {
	r.Get("/tenants", d.ListTenantsAdmin)
	r.Post("/tenants", d.CreateTenantAdmin)

	r.Get("/cluster", d.GetCluster)
	r.Get("/cluster/nextid", d.GetNextID)

	r.Get("/nodes", d.ListNodes)
	r.Get("/nodes/{node}", d.GetNode)
	r.Get("/nodes/{node}/metrics", d.GetNodeMetrics)
	r.Get("/nodes/{node}/bridges", d.GetNodeBridges)
	r.Get("/nodes/{node}/storages", d.GetNodeStorages)
	r.Get("/nodes/{node}/storages/{storage}/content", d.GetStorageContent)

	r.Get("/tenants/{tenantId}/quota", d.GetAdminTenantQuota)
	r.Put("/tenants/{tenantId}/quota", d.PutAdminTenantQuota)

	r.Get("/resources", d.ListResourcesAdmin)
	r.Get("/pools", d.ListPools)
	r.Get("/storage", d.ListStorage)

	r.Get("/tasks", d.ListTasks)
	r.Get("/tasks/{upid}", d.GetTask)
	r.Get("/tasks/{upid}/log", d.GetTaskLog)
}

// MountTenant attaches the tenant-scoped routes. Every route carries the full
// /tenants/{tenantId} prefix because the caller mounts this as an INLINE group
// (not a subrouter): that keeps the authz chain
// (ResolveTenant → ResolveScope → Enforce → AuditOnMutation) running at the
// fully-resolved endpoint so chi's RoutePattern is complete for the Enforce
// lookup. All {vmid} routes are ownership-checked by ResolveScope;
// AuditOnMutation self-gates to non-GET (the mutating choke-point).
func (d *Deps) MountTenant(r chi.Router) {
	const p = "/tenants/{tenantId}"

	// --- read surface (Reader/Contributor) ---
	r.Get(p+"/summary", d.GetTenantSummary)
	r.Get(p+"/quota", d.GetTenantQuota)
	r.Get(p+"/activity", d.GetActivity)
	r.Get(p+"/projects", d.ListProjects)
	r.Get(p+"/projects/{projectId}", d.GetProject)
	r.Get(p+"/projects/{projectId}/quota", d.GetProjectQuota)
	r.Get(p+"/members", d.ListMembers)
	r.Get(p+"/invitations", d.ListInvitations)
	r.Get(p+"/resources", d.ListTenantResources)

	r.Get(p+"/catalog/nextid", d.GetNextID)
	r.Get(p+"/catalog/nodes", d.ListCatalogNodes)
	r.Get(p+"/catalog/nodes/{node}/bridges", d.GetNodeBridges)
	r.Get(p+"/catalog/nodes/{node}/storages", d.ListCatalogStorages)
	r.Get(p+"/catalog/nodes/{node}/storages/{storage}/content", d.GetStorageContent)

	r.Get(p+"/guests/{node}/{type}/{vmid}", d.GetGuest)
	r.Get(p+"/guests/{node}/{type}/{vmid}/metrics", d.GetGuestMetrics)
	r.Get(p+"/guests/{node}/{type}/{vmid}/interfaces", d.GetGuestInterfaces)
	r.Get(p+"/guests/{node}/{type}/{vmid}/snapshots", d.ListSnapshots)
	r.Get(p+"/guests/{node}/{type}/{vmid}/firewall", d.GetGuestFirewall)
	r.Get(p+"/guests/{node}/{type}/{vmid}/acl", d.GetGuestACL)
	r.Get(p+"/guests/{node}/{type}/{vmid}/schedule", d.GetResourceSchedule)
	r.Get(p+"/guests/{node}/{type}/{vmid}/ttl", d.GetGuestTTL)
	r.Get(p+"/projects/{projectId}/schedule", d.GetProjectSchedule)
	r.Get(p+"/projects/{projectId}/ttl-policy", d.GetProjectTTLPolicy)
	r.Get(p+"/projects/{projectId}/ttls", d.ListProjectTTLs)

	// Service catalog (ADR-0026). Always mounted (so the permission/audit
	// completeness tests see them); the handlers 404 when CATALOG_ENABLED is off.
	r.Get(p+"/service-catalog", d.ListServices)
	r.Get(p+"/service-catalog/{serviceId}", d.GetService)

	// Deployment sets (ADR-0029/0030). Always mounted; the handlers 404 when the
	// feature (DEPLOYMENT_SETS_ENABLED + a loaded catalog) is off, mirroring the
	// catalog. {setId} is tenant-level — the handler does its own tenant-filtered 404.
	r.Get(p+"/deployment-sets", d.ListSets)
	r.Get(p+"/deployment-sets/{setId}", d.GetSet)

	r.Get(p+"/deployments/{id}", d.GetDeployment)
	r.Get(p+"/tasks", d.ListTenantTasks)
	r.Get(p+"/tasks/{upid}", d.GetTenantTask)
	r.Get(p+"/tasks/{upid}/log", d.GetTenantTaskLog)

	// --- mutating surface (Contributor/Owner) — gated by AuditOnMutation ---
	r.Post(p+"/invitations", d.CreateInvitation)
	r.Delete(p+"/invitations/{invitationId}", d.RevokeInvitation)

	r.Post(p+"/projects", d.CreateProject)
	r.Patch(p+"/projects/{projectId}", d.RenameProject)
	r.Delete(p+"/projects/{projectId}", d.DeleteProject)
	r.Put(p+"/projects/{projectId}/quota", d.PutProjectQuota)
	r.Put(p+"/projects/{projectId}/schedule", d.PutProjectSchedule)
	r.Delete(p+"/projects/{projectId}/schedule", d.DeleteProjectSchedule)
	r.Put(p+"/projects/{projectId}/ttl-policy", d.PutProjectTTLPolicy)

	r.Post(p+"/service-catalog/{serviceId}/provision", d.ProvisionService)

	// Deployment sets (ADR-0029/0030): create, start/stop fan-out, delete.
	r.Post(p+"/deployment-sets", d.CreateSet)
	r.Delete(p+"/deployment-sets/{setId}", d.DeleteSet)
	r.Post(p+"/deployment-sets/{setId}/{action}", d.SetAction)

	r.Post(p+"/guests", d.CreateGuest)
	r.Patch(p+"/guests/{node}/{type}/{vmid}/config", d.UpdateGuestConfig)
	r.Post(p+"/guests/{node}/{type}/{vmid}/resize", d.ResizeGuestDisk)
	r.Post(p+"/guests/{node}/{type}/{vmid}/snapshots", d.CreateSnapshot)
	r.Post(p+"/guests/{node}/{type}/{vmid}/snapshots/{name}/rollback", d.RollbackSnapshot)
	r.Delete(p+"/guests/{node}/{type}/{vmid}/snapshots/{name}", d.DeleteSnapshot)
	r.Put(p+"/guests/{node}/{type}/{vmid}/firewall/options", d.SetGuestFirewall)
	r.Post(p+"/guests/{node}/{type}/{vmid}/console", d.OpenConsole)
	r.Put(p+"/guests/{node}/{type}/{vmid}/schedule", d.PutResourceSchedule)
	r.Delete(p+"/guests/{node}/{type}/{vmid}/schedule", d.DeleteResourceSchedule)
	r.Post(p+"/guests/{node}/{type}/{vmid}/schedule/skip", d.SkipNextSchedule)
	r.Put(p+"/guests/{node}/{type}/{vmid}/ttl", d.PutGuestTTL)
	r.Delete(p+"/guests/{node}/{type}/{vmid}/ttl", d.DeleteGuestTTL)
	r.Post(p+"/guests/{node}/{type}/{vmid}/ttl/extend", d.ExtendGuestTTL)
	r.Post(p+"/guests/{node}/{type}/{vmid}/{action}", d.GuestAction)
	r.Delete(p+"/guests/{node}/{type}/{vmid}", d.DeleteGuest)
}

// Health probe tuning: a short deadline so /api/health stays snappy even
// when Proxmox is down, and a cache so the endpoint (often polled by
// compose/uptime checks) does not hammer PVE.
const (
	healthProbeTimeout = 3 * time.Second
	healthCacheTTL     = 30 * time.Second
)

// Health returns the public /api/health handler: the API's own liveness plus
// Proxmox reachability probed via /version and cached for healthCacheTTL.
func (d *Deps) Health() http.HandlerFunc {
	var (
		mu       sync.Mutex
		cached   string
		cachedAt time.Time
	)
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if cached == "" || time.Since(cachedAt) >= healthCacheTTL {
			ctx, cancel := context.WithTimeout(r.Context(), healthProbeTimeout)
			_, err := d.PVE.Version(ctx)
			cancel()
			if err != nil {
				cached = "unreachable"
			} else {
				cached = "ok"
			}
			cachedAt = time.Now()
		}
		pveStatus := cached
		mu.Unlock()

		// Status stays "ok": the API itself answered; Proxmox reachability
		// is reported separately so the UI can show a precise banner.
		httpserver.WriteJSON(w, http.StatusOK, types.Health{Status: "ok", Proxmox: pveStatus})
	}
}

func (d *Deps) logger() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// splitPVEList splits PVE's joined lists (tags "a;b", content "iso,backup")
// into a clean, never-nil slice. PVE canonically joins tags with semicolons
// but commas appear in the wild; both are accepted.
func splitPVEList(s string) []string {
	out := []string{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
