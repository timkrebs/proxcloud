package handlers

import (
	"encoding/json"
	"net/http"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// ListNotifications serves GET /api/notifications (newest first).
func (d *Deps) ListNotifications(w http.ResponseWriter, _ *http.Request) {
	if d.Registry == nil {
		httpserver.WriteJSON(w, http.StatusOK, []types.Notification{})
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, d.Registry.Notifications())
}

// MarkNotificationsRead serves POST /api/notifications/read.
func (d *Deps) MarkNotificationsRead(w http.ResponseWriter, r *http.Request) {
	var req types.MarkReadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: "body must be JSON with an ids array", Status: http.StatusBadRequest})
		return
	}
	if d.Registry != nil {
		d.Registry.MarkRead(req.IDs)
	}
	w.WriteHeader(http.StatusNoContent)
}
