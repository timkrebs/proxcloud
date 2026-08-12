// Package authz holds the route permission table — the single registry that
// declares, for every mounted (HTTP method, chi route pattern), what access a
// request needs. Phase 1 ships the registry plus a completeness test that fails
// if any mounted route lacks an entry (or the registry names a route that no
// longer exists); the enforce middleware that consumes it arrives in Phase 3.
//
// The permission table is a durable guardrail: no route ships without an entry,
// so every future route is forced to declare its rule.
package authz

import "net/http"

// Rule is the access requirement for a route. Phase 1 only distinguishes public
// from authenticated; the tenancy roles (Phase 3) slot in as higher values
// without changing the registry shape or the completeness test.
type Rule int

const (
	// PublicRule requires no authentication (health, login, one-shot console).
	PublicRule Rule = iota
	// AuthenticatedRule requires a valid session but no tenancy role yet.
	// Every scoped route is AuthenticatedRule in Phase 1; Phase 3 refines the
	// tenant-scoped ones to the role constants below.
	AuthenticatedRule

	// --- reserved for Phase 3 (tenancy roles); not yet enforced ---

	// ReaderRule requires at least a Reader role in the active scope.
	ReaderRule
	// ContributorRule requires at least a Contributor role (CRUD resources).
	ContributorRule
	// OwnerRule requires the Owner role (full + member management).
	OwnerRule
	// PlatformAdminRule requires the platform-admin flag (cross-tenant ops).
	PlatformAdminRule
)

// String renders a Rule for diagnostics.
func (r Rule) String() string {
	switch r {
	case PublicRule:
		return "public"
	case AuthenticatedRule:
		return "authenticated"
	case ReaderRule:
		return "reader"
	case ContributorRule:
		return "contributor"
	case OwnerRule:
		return "owner"
	case PlatformAdminRule:
		return "platform_admin"
	default:
		return "unknown"
	}
}

// Permission binds a route to its Rule.
type Permission struct {
	Method  string // HTTP method, e.g. http.MethodGet
	Pattern string // full chi route pattern, e.g. "/api/guests/{node}/{type}/{vmid}"
	Rule    Rule
}

type routeKey struct {
	Method  string
	Pattern string
}

// registry is the authoritative permission table. Patterns are the full chi
// route patterns (including the /api prefix) as reported by
// chi.RouteContext().RoutePattern() at request time and by chi.Walk in tests.
var registry = buildRegistry()

func buildRegistry() map[routeKey]Rule {
	perms := []Permission{
		// --- public: no session required ---
		{http.MethodGet, "/api/health", PublicRule},
		// Build metadata (WS3): git commit/semver/build time of the running
		// binary. Public build info only — no secrets — so the CD smoke test and
		// the frontend footer can read it without a session.
		{http.MethodGet, "/api/v1/version", PublicRule},
		{http.MethodGet, "/api/auth/bootstrap-status", PublicRule},
		{http.MethodPost, "/api/auth/bootstrap", PublicRule}, // hard-guarded: 409 once any user exists
		{http.MethodPost, "/api/auth/login", PublicRule},
		// Second-factor login (ADR-0013 §3): the interim proxcloud_totp challenge
		// cookie is the credential; the caller is not yet signed in, so it is public.
		{http.MethodPost, "/api/auth/login/totp", PublicRule},
		// Invitation accept surface (ADR-0013 §2): validate + accept are public —
		// the opaque token is the credential; the caller may be signed out or new.
		{http.MethodGet, "/api/auth/invitations/{token}", PublicRule},
		{http.MethodPost, "/api/auth/invitations/{token}/accept", PublicRule},
		// Console websocket authenticates via a one-shot, single-use session id
		// in the path (not the cookie), so it is public at the router level.
		{http.MethodGet, "/api/console/ws/{sessionId}", PublicRule},

		// --- authenticated / tenant-agnostic account + stream surface ---
		{http.MethodPost, "/api/auth/logout", AuthenticatedRule},
		{http.MethodGet, "/api/auth/me", AuthenticatedRule},
		{http.MethodPatch, "/api/auth/active-tenant", AuthenticatedRule},
		{http.MethodPost, "/api/auth/password", AuthenticatedRule},
		{http.MethodGet, "/api/auth/sessions", AuthenticatedRule},
		{http.MethodDelete, "/api/auth/sessions/{id}", AuthenticatedRule},
		// Account-level TOTP + recovery management (ADR-0013 §3). Not on the
		// tenant subtree → audited by auditz in the handlers, not AuditOnMutation.
		{http.MethodPost, "/api/auth/totp/enroll", AuthenticatedRule},
		{http.MethodPost, "/api/auth/totp/verify", AuthenticatedRule},
		{http.MethodPost, "/api/auth/totp/disable", AuthenticatedRule},
		{http.MethodPost, "/api/auth/totp/recovery-codes", AuthenticatedRule},
		{http.MethodGet, "/api/events", AuthenticatedRule},
		{http.MethodGet, "/api/notifications", AuthenticatedRule},
		{http.MethodPost, "/api/notifications/read", AuthenticatedRule},
		{http.MethodGet, "/api/pricing", AuthenticatedRule},

		// --- platform-admin: /api/admin/* ---
		{http.MethodGet, "/api/admin/tenants", PlatformAdminRule},
		{http.MethodPost, "/api/admin/tenants", PlatformAdminRule},
		{http.MethodGet, "/api/admin/cluster", PlatformAdminRule},
		{http.MethodGet, "/api/admin/cluster/nextid", PlatformAdminRule},
		{http.MethodGet, "/api/admin/nodes", PlatformAdminRule},
		{http.MethodGet, "/api/admin/nodes/{node}", PlatformAdminRule},
		{http.MethodGet, "/api/admin/nodes/{node}/metrics", PlatformAdminRule},
		{http.MethodGet, "/api/admin/nodes/{node}/bridges", PlatformAdminRule},
		{http.MethodGet, "/api/admin/nodes/{node}/storages", PlatformAdminRule},
		{http.MethodGet, "/api/admin/nodes/{node}/storages/{storage}/content", PlatformAdminRule},
		{http.MethodGet, "/api/admin/resources", PlatformAdminRule},
		{http.MethodGet, "/api/admin/pools", PlatformAdminRule},
		{http.MethodGet, "/api/admin/storage", PlatformAdminRule},
		{http.MethodGet, "/api/admin/tasks", PlatformAdminRule},
		{http.MethodGet, "/api/admin/tasks/{upid}", PlatformAdminRule},
		{http.MethodGet, "/api/admin/tasks/{upid}/log", PlatformAdminRule},
		{http.MethodGet, "/api/admin/tenants/{tenantId}/quota", PlatformAdminRule},
		{http.MethodPut, "/api/admin/tenants/{tenantId}/quota", PlatformAdminRule},

		// --- tenant-scoped: /api/tenants/{tenantId}/* ---
		{http.MethodGet, "/api/tenants/{tenantId}/summary", ReaderRule},
		{http.MethodGet, "/api/tenants/{tenantId}/quota", ReaderRule},
		{http.MethodGet, "/api/tenants/{tenantId}/activity", ReaderRule},
		{http.MethodGet, "/api/tenants/{tenantId}/projects", ReaderRule},
		{http.MethodPost, "/api/tenants/{tenantId}/projects", OwnerRule},
		{http.MethodGet, "/api/tenants/{tenantId}/projects/{projectId}", ReaderRule},
		{http.MethodPatch, "/api/tenants/{tenantId}/projects/{projectId}", OwnerRule},
		{http.MethodDelete, "/api/tenants/{tenantId}/projects/{projectId}", OwnerRule},
		{http.MethodGet, "/api/tenants/{tenantId}/projects/{projectId}/quota", ReaderRule},
		{http.MethodPut, "/api/tenants/{tenantId}/projects/{projectId}/quota", OwnerRule},
		{http.MethodGet, "/api/tenants/{tenantId}/members", OwnerRule},
		{http.MethodGet, "/api/tenants/{tenantId}/invitations", OwnerRule},
		{http.MethodPost, "/api/tenants/{tenantId}/invitations", OwnerRule},
		{http.MethodDelete, "/api/tenants/{tenantId}/invitations/{invitationId}", OwnerRule},
		{http.MethodGet, "/api/tenants/{tenantId}/resources", ReaderRule},
		{http.MethodGet, "/api/tenants/{tenantId}/catalog/nextid", ContributorRule},
		{http.MethodGet, "/api/tenants/{tenantId}/catalog/nodes", ContributorRule},
		{http.MethodGet, "/api/tenants/{tenantId}/catalog/nodes/{node}/bridges", ContributorRule},
		{http.MethodGet, "/api/tenants/{tenantId}/catalog/nodes/{node}/storages", ContributorRule},
		{http.MethodGet, "/api/tenants/{tenantId}/catalog/nodes/{node}/storages/{storage}/content", ContributorRule},
		{http.MethodPost, "/api/tenants/{tenantId}/guests", ContributorRule},
		{http.MethodGet, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}", ReaderRule},
		{http.MethodPatch, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/config", ContributorRule},
		{http.MethodGet, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/metrics", ReaderRule},
		{http.MethodGet, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/interfaces", ReaderRule},
		{http.MethodPost, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/resize", ContributorRule},
		{http.MethodGet, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/snapshots", ReaderRule},
		{http.MethodPost, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/snapshots", ContributorRule},
		{http.MethodPost, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/snapshots/{name}/rollback", ContributorRule},
		{http.MethodDelete, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/snapshots/{name}", ContributorRule},
		{http.MethodGet, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/firewall", ReaderRule},
		{http.MethodPut, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/firewall/options", ContributorRule},
		{http.MethodGet, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/acl", ReaderRule},
		{http.MethodPost, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/{action}", ContributorRule},
		{http.MethodDelete, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}", ContributorRule},
		{http.MethodPost, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/console", ContributorRule},
		{http.MethodGet, "/api/tenants/{tenantId}/deployments/{id}", ReaderRule},
		{http.MethodGet, "/api/tenants/{tenantId}/tasks", ReaderRule},
		{http.MethodGet, "/api/tenants/{tenantId}/tasks/{upid}", ReaderRule},
		{http.MethodGet, "/api/tenants/{tenantId}/tasks/{upid}/log", ReaderRule},
	}

	m := make(map[routeKey]Rule, len(perms))
	for _, p := range perms {
		m[routeKey{p.Method, p.Pattern}] = p.Rule
	}
	return m
}

// Lookup returns the Rule for a (method, pattern) and whether it is registered.
func Lookup(method, pattern string) (Rule, bool) {
	r, ok := registry[routeKey{method, pattern}]
	return r, ok
}

// Registered returns every permission in the table (for tests/introspection).
func Registered() []Permission {
	out := make([]Permission, 0, len(registry))
	for k, r := range registry {
		out = append(out, Permission{Method: k.Method, Pattern: k.Pattern, Rule: r})
	}
	return out
}
