package handlers

import (
	"encoding/json"
	"net/http"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// ListNotifications serves GET /api/notifications (newest first), scoped to the
// caller's active tenant. The notification ring is process-global, so an
// unfiltered return would leak every tenant's task activity (tenancy iron rule
// #1). Filtering compares the tenant STORED on each entry when the task was
// tracked — not a live owned-VMID set — so a VMID freed by one tenant and
// reissued to another never surfaces the previous owner's history. Platform
// admins see all; a caller with no active tenant sees an empty list.
func (d *Deps) ListNotifications(w http.ResponseWriter, r *http.Request) {
	if d.Registry == nil {
		httpserver.WriteJSON(w, http.StatusOK, []types.Notification{})
		return
	}
	id, ok := requireIdentity(w, r)
	if !ok {
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, d.Registry.Notifications(id.ActiveTenantID, id.IsPlatformAdmin))
}

// MarkNotificationsRead serves POST /api/notifications/read. It marks only
// notifications stored under the caller's tenant, so one tenant cannot flip
// another's — including entries left on a VMID the caller later inherited.
func (d *Deps) MarkNotificationsRead(w http.ResponseWriter, r *http.Request) {
	var req types.MarkReadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: "body must be JSON with an ids array", Status: http.StatusBadRequest})
		return
	}
	id, ok := requireIdentity(w, r)
	if !ok {
		return
	}
	if d.Registry != nil {
		d.Registry.MarkRead(req.IDs, id.ActiveTenantID, id.IsPlatformAdmin)
	}
	w.WriteHeader(http.StatusNoContent)
}
