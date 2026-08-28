package authz

import "net/http"

// The audit action-map is the companion to the permission table: every mutating
// (non-GET) tenant route declares BOTH a minimum role (registry) and an audit
// action here. A mutating route with no action entry makes AuditOnMutation 500
// and log loudly; the completeness test (audit_completeness_test.go) fails CI
// before that can ship — so the map can never silently drift behind the router.
//
// Actions are stable, dotted verbs (resource.verb) consumed by the activity log.
// Only the tenant-scoped mutating subtree is covered here. Admin mutations
// (e.g. tenant.create, tenant.quota.update) run outside this middleware, so they
// are audited at the handler level via internal/auditz — the same fail-closed
// intent→finalize contract, just not driven by this map (plan §4).

// guestActionPattern is the wildcard lifecycle route (start/stop/reboot/…); its
// concrete verb lives in the {action} path param, so AuditAction refines the base
// action from urlParam when present.
const guestActionPattern = "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/{action}"

// auditActions maps a mutating tenant route to its audit action verb.
var auditActions = map[routeKey]string{
	{http.MethodPost, "/api/tenants/{tenantId}/projects"}:                  "project.create",
	{http.MethodPatch, "/api/tenants/{tenantId}/projects/{projectId}"}:     "project.rename",
	{http.MethodDelete, "/api/tenants/{tenantId}/projects/{projectId}"}:    "project.delete",
	{http.MethodPut, "/api/tenants/{tenantId}/projects/{projectId}/quota"}: "project.quota.update",

	{http.MethodPut, "/api/tenants/{tenantId}/projects/{projectId}/schedule"}:    "project.schedule.update",
	{http.MethodDelete, "/api/tenants/{tenantId}/projects/{projectId}/schedule"}: "project.schedule.delete",

	{http.MethodPut, "/api/tenants/{tenantId}/projects/{projectId}/ttl-policy"}: "project.ttl_policy.update",

	{http.MethodPost, "/api/tenants/{tenantId}/invitations"}:                  "invitation.create",
	{http.MethodDelete, "/api/tenants/{tenantId}/invitations/{invitationId}"}: "invitation.revoke",

	{http.MethodPost, "/api/tenants/{tenantId}/service-catalog/{serviceId}/provision"}: "service_catalog.provision",

	{http.MethodPost, "/api/tenants/{tenantId}/guests"}:                                                "guest.create",
	{http.MethodPatch, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/config"}:                   "guest.config.update",
	{http.MethodPost, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/resize"}:                    "guest.disk.resize",
	{http.MethodPost, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/snapshots"}:                 "guest.snapshot.create",
	{http.MethodPost, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/snapshots/{name}/rollback"}: "guest.snapshot.rollback",
	{http.MethodDelete, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/snapshots/{name}"}:        "guest.snapshot.delete",
	{http.MethodPut, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/firewall/options"}:           "guest.firewall.update",
	{http.MethodPost, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/console"}:                   "guest.console.open",
	{http.MethodPut, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/schedule"}:                   "guest.schedule.update",
	{http.MethodDelete, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/schedule"}:                "guest.schedule.delete",
	{http.MethodPost, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/schedule/skip"}:             "guest.schedule.skip",
	{http.MethodPut, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/ttl"}:                        "guest.ttl.update",
	{http.MethodDelete, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/ttl"}:                     "guest.ttl.clear",
	{http.MethodPost, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/ttl/extend"}:                "ttl.extend",
	{http.MethodPost, guestActionPattern}:                                                              "guest.action",
	{http.MethodDelete, "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}"}:                         "guest.delete",
}

// AuditAction returns the audit action for a (method, chi-route-pattern), or ""
// when the route has no entry (which AuditOnMutation treats as fail-closed 500).
// For the wildcard lifecycle route the concrete verb (start/stop/…) is read from
// urlParam("action") and appended, e.g. "guest.start"; urlParam may be nil (the
// completeness test calls it without a request), in which case the base is used.
func AuditAction(method, pattern string, urlParam func(string) string) string {
	base, ok := auditActions[routeKey{method, pattern}]
	if !ok {
		return ""
	}
	if pattern == guestActionPattern && urlParam != nil {
		if a := urlParam("action"); a != "" {
			return "guest." + a
		}
	}
	return base
}

// auditTarget derives the (target_type, target_id) of a mutation from its path
// params: a {vmid} route targets the guest, a {projectId} route the project.
// Creates carry the id in the body (unknown at intent time) → both nil.
func auditTarget(urlParam func(string) string) (targetType, targetID *string) {
	if urlParam == nil {
		return nil, nil
	}
	if v := urlParam("vmid"); v != "" {
		t := "guest"
		return &t, &v
	}
	if v := urlParam("projectId"); v != "" {
		t := "project"
		return &t, &v
	}
	return nil, nil
}
