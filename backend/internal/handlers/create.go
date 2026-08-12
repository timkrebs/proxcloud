package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/bootstrap"
	"github.com/timkrebs9/proxcloud/backend/internal/deploy"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// CreateGuest serves POST /api/tenants/{tenantId}/guests. It resolves the
// mandatory projectId to its pool, ensures the pool exists, verifies a clone
// source is owned in this tenant, inserts a PENDING ownership reservation, then
// submits the deployment. The reservation is finalized on task success and
// released on failure (honest task states — never faked).
func (d *Deps) CreateGuest(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	if d.Deploy == nil {
		httpserver.WriteError(w, &types.APIError{Code: "internal", Message: "deployment engine not configured", Status: http.StatusInternalServerError})
		return
	}
	var req types.CreateGuestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, badRequest("body must be a JSON CreateGuestRequest"))
		return
	}
	tenantID := id.ActiveTenantID

	if req.ProjectId == "" {
		httpserver.WriteError(w, badRequest("projectId is required"))
		return
	}
	// Resolve the project → pool. Cross-tenant project → 404 (no existence leak).
	proj, err := d.Store.GetProjectByID(r.Context(), req.ProjectId)
	if errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if proj.TenantID != tenantID {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}

	// Validate before we reserve, so a bad request never leaves an orphan row.
	if err := deploy.Validate(&req); err != nil {
		httpserver.WriteError(w, badRequest(err.Error()))
		return
	}

	// Clone source must be owned in THIS tenant (IDOR guard on the template).
	cloneOK := false
	if req.Source.Mode == "clone" {
		if _, err := authz.ResolveOwnership(r.Context(), d.Store, req.Source.CloneVMID, tenantID); err != nil {
			httpserver.WriteError(w, notFound("Clone source not found."))
			return
		}
		cloneOK = true
	}

	// Ensure the pool exists, then derive it onto the request (ignoring any
	// client-supplied pool). Surfaces the verbatim PVE error on failure.
	if err := bootstrap.EnsureProjectPool(r.Context(), d.PVE, proj.PoolID, poolComment); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	req.Pool = proj.PoolID

	// Reserve the VMID (pending) before submitting. A duplicate VMID is a conflict.
	actor := id.UserID
	own, err := d.Store.CreateOwnership(r.Context(), store.CreateOwnershipParams{
		TenantID:  tenantID,
		ProjectID: proj.ID,
		VMID:      req.VMID,
		GuestType: req.Type,
		Node:      req.Node,
		CreatedBy: &actor,
		Status:    "pending",
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			httpserver.WriteError(w, &types.APIError{
				Code:    "conflict",
				Message: "That VMID is already reserved or in use.",
				Status:  http.StatusConflict,
			})
			return
		}
		// A real store failure is never silently a 409 — log it and fail 500.
		d.logger().Error("create ownership", "vmid", req.VMID, "err", err)
		httpserver.WriteError(w, &types.APIError{
			Code:    "internal",
			Message: "Failed to reserve the VMID.",
			Status:  http.StatusInternalServerError,
		})
		return
	}

	cctx := deploy.CreateContext{
		TenantID:      tenantID,
		ProjectID:     proj.ID,
		PoolID:        proj.PoolID,
		ActorUserID:   actor,
		OwnershipID:   own.ID,
		CloneSourceOK: cloneOK,
	}
	dep, err := d.Deploy.Submit(&req, cctx)
	if err != nil {
		// Submit rejected the request before starting: free the reservation.
		if relErr := d.Store.ReleaseOwnership(r.Context(), own.ID); relErr != nil {
			d.logger().Warn("release ownership after submit failure", "err", relErr)
		}
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusAccepted, types.CreateGuestResponse{DeploymentID: dep.ID, VMID: dep.VMID})
}

// GetDeployment serves GET /api/tenants/{tenantId}/deployments/{id}. The
// deployment's VMID must be owned by the active tenant (404 otherwise) — the
// path carries no {vmid}, so the check is explicit here.
func (d *Deps) GetDeployment(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok {
		return
	}
	if d.Deploy == nil {
		httpserver.WriteError(w, notFound("deployment not found"))
		return
	}
	dep, found := d.Deploy.Get(chi.URLParam(r, "id"))
	if !found {
		httpserver.WriteError(w, &types.APIError{
			Code:    "not_found",
			Message: "Deployment not found — deployment progress does not survive a backend restart; check the activity log for the task itself.",
			Status:  http.StatusNotFound,
		})
		return
	}
	// Ownership check: the deployment's guest must belong to this tenant.
	if d.Store != nil {
		if _, err := authz.ResolveOwnership(r.Context(), d.Store, dep.VMID, id.ActiveTenantID); err != nil {
			httpserver.WriteError(w, notFound("Deployment not found."))
			return
		}
	}
	httpserver.WriteJSON(w, http.StatusOK, dep)
}
