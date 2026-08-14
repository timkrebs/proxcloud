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
// #1); we filter to the tenant's owned VMIDs, mirroring the SSE feed. Platform
// admins see all; without a store we cannot scope, so fail closed to empty.
func (d *Deps) ListNotifications(w http.ResponseWriter, r *http.Request) {
	if d.Registry == nil {
		httpserver.WriteJSON(w, http.StatusOK, []types.Notification{})
		return
	}
	id, ok := requireIdentity(w, r)
	if !ok {
		return
	}
	if id.IsPlatformAdmin {
		httpserver.WriteJSON(w, http.StatusOK, d.Registry.Notifications(nil, true))
		return
	}
	if d.Store == nil {
		httpserver.WriteJSON(w, http.StatusOK, []types.Notification{})
		return
	}
	owned, err := d.tenantOwnedVMIDs(r.Context(), id.ActiveTenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, d.Registry.Notifications(owned, false))
}

// MarkNotificationsRead serves POST /api/notifications/read. It marks only
// notifications the caller's tenant owns, so one tenant cannot flip another's.
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
	if d.Registry == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if id.IsPlatformAdmin {
		d.Registry.MarkRead(req.IDs, nil, true)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if d.Store != nil {
		owned, err := d.tenantOwnedVMIDs(r.Context(), id.ActiveTenantID)
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
		d.Registry.MarkRead(req.IDs, owned, false)
	}
	w.WriteHeader(http.StatusNoContent)
}
