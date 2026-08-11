package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// CreateGuest serves POST /api/guests: validates, starts the deployment,
// answers 202 with the deployment id the progress page polls.
func (d *Deps) CreateGuest(w http.ResponseWriter, r *http.Request) {
	if d.Deploy == nil {
		httpserver.WriteError(w, &types.APIError{Code: "internal", Message: "deployment engine not configured", Status: http.StatusInternalServerError})
		return
	}
	var req types.CreateGuestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: "body must be a JSON CreateGuestRequest", Status: http.StatusBadRequest})
		return
	}
	dep, err := d.Deploy.Submit(&req)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusAccepted, types.CreateGuestResponse{DeploymentID: dep.ID, VMID: dep.VMID})
}

// GetDeployment serves GET /api/deployments/{id}.
func (d *Deps) GetDeployment(w http.ResponseWriter, r *http.Request) {
	if d.Deploy == nil {
		httpserver.WriteError(w, &types.APIError{Code: "not_found", Message: "deployment not found", Status: http.StatusNotFound})
		return
	}
	dep, ok := d.Deploy.Get(chi.URLParam(r, "id"))
	if !ok {
		httpserver.WriteError(w, &types.APIError{
			Code:    "not_found",
			Message: "Deployment not found — deployment progress does not survive a backend restart; check the activity log for the task itself.",
			Status:  http.StatusNotFound,
		})
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, dep)
}
