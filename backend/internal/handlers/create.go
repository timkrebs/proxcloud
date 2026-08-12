package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/bootstrap"
	"github.com/timkrebs9/proxcloud/backend/internal/deploy"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// quotaExceededMessage renders the contract's 409 quota_exceeded message from the
// store's verdict, naming the scope, the violated dimension, and used/limit so the
// wizard can surface exactly which cap blocked the create.
func quotaExceededMessage(e store.ErrQuotaExceeded) string {
	scope := "Tenant"
	if e.Scope == "project" {
		scope = "Project"
	}
	return fmt.Sprintf("%s %s quota exceeded: %d in use of %d, %d requested.",
		scope, quotaDimensionLabel(e.Dimension), e.Used, e.Limit, e.Requested)
}

// quotaDimensionLabel maps a store dimension key to a human label for messages.
func quotaDimensionLabel(dim string) string {
	switch dim {
	case "vcpu":
		return "vCPU"
	case "ram_mb":
		return "RAM (MB)"
	case "disk_gb":
		return "disk (GB)"
	case "count":
		return "resource count"
	default:
		return dim
	}
}

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

	actor := id.UserID

	// Fetch the live allocation snapshot ONCE before the reservation — it is both
	// the quota-usage join for active guests and the source of the clone delta, and
	// fetching it here keeps every PVE round-trip OUT of the store's per-tenant
	// advisory lock (ADR-0009).
	snap, err := d.clusterSnapshot(r)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	// Requested delta: a fresh create uses the wizard sizing; a clone copies the
	// source template's provisioned allocation from the snapshot (ADR-0012 §2.2/§4).
	// The clone source is already ownership-checked above, so a snapshot MISS here is
	// a transient PVE/drift condition, not an auth issue — never reserve a zero delta
	// (that would be the least-conservative outcome, the exact "counts as zero, all
	// pass" hole reserved_* exists to close). Reject with a retryable 400 instead.
	reserved := store.Alloc{VCPU: req.Cores, RAMMB: req.MemoryMB, DiskGB: int64(req.DiskGB)}
	if req.Source.Mode == "clone" {
		src, ok := snap[req.Source.CloneVMID]
		if !ok {
			httpserver.WriteError(w, badRequest("clone source is currently unavailable; try again"))
			return
		}
		reserved = src
	}

	// Reserve the VMID under the concurrency-safe quota check (pending row with
	// reserved_* set). Over-quota → 409 quota_exceeded; duplicate VMID → 409
	// conflict. On EITHER, and on any store failure, NO Proxmox create call has run
	// (the reservation precedes EnsureProjectPool + Submit).
	own, err := d.Store.ReserveOwnership(r.Context(), store.ReserveOwnershipParams{
		TenantID:  tenantID,
		ProjectID: proj.ID,
		VMID:      req.VMID,
		GuestType: req.Type,
		Node:      req.Node,
		CreatedBy: &actor,
		Reserved:  reserved,
		Snapshot:  snap,
	})
	if err != nil {
		var qe store.ErrQuotaExceeded
		switch {
		case errors.As(err, &qe):
			httpserver.WriteError(w, &types.APIError{
				Code:    "quota_exceeded",
				Message: quotaExceededMessage(qe),
				Status:  http.StatusConflict,
			})
		case errors.Is(err, store.ErrConflict):
			httpserver.WriteError(w, &types.APIError{
				Code:    "conflict",
				Message: "That VMID is already reserved or in use.",
				Status:  http.StatusConflict,
			})
		default:
			// A real store failure is never silently a 409 — log it and fail 500.
			d.logger().Error("reserve ownership", "vmid", req.VMID, "err", err)
			httpserver.WriteError(w, &types.APIError{
				Code:    "internal",
				Message: "Failed to reserve the VMID.",
				Status:  http.StatusInternalServerError,
			})
		}
		return
	}

	// Ensure the pool exists AFTER the reservation clears, then derive it onto the
	// request (ignoring any client-supplied pool). A pool failure frees the
	// just-made reservation so it does not leak quota.
	if err := bootstrap.EnsureProjectPool(r.Context(), d.PVE, proj.PoolID, poolComment); err != nil {
		if relErr := d.Store.ReleaseOwnership(r.Context(), own.ID); relErr != nil {
			d.logger().Warn("release ownership after pool failure", "err", relErr)
		}
		httpserver.WriteError(w, err)
		return
	}
	req.Pool = proj.PoolID

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
	// Enrich the audit row's detail with the resolved guest (best-effort; never
	// load-bearing for the one-row audit guarantee — a no-op outside the choke-point).
	authz.Annotate(r.Context(), "vmid", strconv.Itoa(dep.VMID))
	authz.Annotate(r.Context(), "name", req.Name)
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
