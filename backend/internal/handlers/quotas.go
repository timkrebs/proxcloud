package handlers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auditz"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// auditz builds a Recorder over the request-scoped store for handler-level audit
// of mutations that live outside the tenant AuditOnMutation subtree (admin
// tenant/quota routes now; Phase-5 account-security routes reuse the same seam).
func (d *Deps) auditz() *auditz.Recorder {
	return &auditz.Recorder{Store: d.Store, Log: d.logger()}
}

// finalizeErr finalizes a pending audit row on a handler error path, recording
// the same HTTP status the client will receive (per httpserver.WriteError).
func finalizeErr(p *auditz.Pending, r *http.Request, err error) {
	status := apiStatus(err)
	p.Finalize(r.Context(), auditz.OutcomeForStatus(status), map[string]any{"status": status})
}

const (
	bytesPerMB = int64(1024 * 1024)
	bytesPerGB = int64(1024 * 1024 * 1024)
)

// snapshotFromResources digests a ClusterResources list into the per-VMID
// allocation map ComputeUsage joins against: an active guest's usage is its
// PROVISIONED capacity (MaxCPU cores, MaxMem→MB, MaxDisk→GB; ADR-0012 §4).
func snapshotFromResources(rows []proxmox.RawResource) map[int]store.Alloc {
	snap := make(map[int]store.Alloc, len(rows))
	for _, row := range rows {
		if row.Type != "qemu" && row.Type != "lxc" {
			continue
		}
		snap[row.VMID] = store.Alloc{
			VCPU:   row.MaxCPU,
			RAMMB:  row.MaxMem / bytesPerMB,
			DiskGB: row.MaxDisk / bytesPerGB,
		}
	}
	return snap
}

// clusterSnapshot fetches ClusterResources once and builds the allocation map.
func (d *Deps) clusterSnapshot(r *http.Request) (map[int]store.Alloc, error) {
	rows, err := d.PVE.ClusterResources(r.Context())
	if err != nil {
		return nil, err
	}
	return snapshotFromResources(rows), nil
}

func toQuotaLimits(q *store.Quota) types.QuotaLimits {
	if q == nil {
		return types.QuotaLimits{}
	}
	return types.QuotaLimits{MaxVCPU: q.MaxVCPU, MaxRAMMB: q.MaxRAMMB, MaxDiskGB: q.MaxDiskGB, MaxCount: q.MaxCount}
}

func remainingInt(limit *int, used int) int {
	if limit == nil {
		return 0
	}
	if r := *limit - used; r > 0 {
		return r
	}
	return 0
}

func remainingInt64(limit *int64, used int64) int64 {
	if limit == nil {
		return 0
	}
	if r := *limit - used; r > 0 {
		return r
	}
	return 0
}

// buildQuotaWithUsage assembles the wire QuotaWithUsage. Remaining is clamped at
// zero and is meaningful only where the matching limit is non-null.
func buildQuotaWithUsage(scopeType, scopeID string, q *store.Quota, u store.QuotaUsage) types.QuotaWithUsage {
	limits := toQuotaLimits(q)
	return types.QuotaWithUsage{
		ScopeType: scopeType,
		ScopeID:   scopeID,
		Limits:    limits,
		Usage:     types.QuotaUsage{VCPU: u.VCPU, RAMMB: u.RAMMB, DiskGB: u.DiskGB, Count: u.Count},
		Remaining: types.QuotaUsage{
			VCPU:   remainingInt(limits.MaxVCPU, u.VCPU),
			RAMMB:  remainingInt64(limits.MaxRAMMB, u.RAMMB),
			DiskGB: remainingInt64(limits.MaxDiskGB, u.DiskGB),
			Count:  remainingInt(limits.MaxCount, u.Count),
		},
	}
}

// getQuotaOrUnlimited fetches a scope's quota, mapping ErrNotFound to (nil, nil)
// so the caller treats a missing row as all-unlimited.
func (d *Deps) getQuotaOrUnlimited(r *http.Request, scopeType, scopeID string) (*store.Quota, error) {
	q, err := d.Store.GetQuota(r.Context(), scopeType, scopeID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return q, err
}

// GetTenantQuota serves GET /api/tenants/{tenantId}/quota (Reader): the tenant's
// limits + live usage for the dashboard bars.
func (d *Deps) GetTenantQuota(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	d.writeTenantQuota(w, r, id.ActiveTenantID)
}

// GetAdminTenantQuota serves GET /api/admin/tenants/{tenantId}/quota (Admin). The
// tenant id comes from the path (this route is outside the ResolveTenant chain);
// a nonexistent tenant is a 404 rather than a fabricated all-zero quota.
func (d *Deps) GetAdminTenantQuota(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireIdentity(w, r); !ok || !d.requireStore(w) {
		return
	}
	tenantID := chi.URLParam(r, "tenantId")
	if _, err := d.Store.GetTenantByID(r.Context(), tenantID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("Tenant not found."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	d.writeTenantQuota(w, r, tenantID)
}

// writeTenantQuota computes usage against a fresh snapshot and writes the tenant
// QuotaWithUsage. Shared by the Reader and Admin tenant-quota reads.
func (d *Deps) writeTenantQuota(w http.ResponseWriter, r *http.Request, tenantID string) {
	snap, err := d.clusterSnapshot(r)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	usage, _, err := d.Store.ComputeUsage(r.Context(), tenantID, snap)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	quota, err := d.getQuotaOrUnlimited(r, "tenant", tenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, buildQuotaWithUsage("tenant", tenantID, quota, usage))
}

// GetProjectQuota serves GET /api/tenants/{tenantId}/projects/{projectId}/quota
// (Reader): the project's limits+usage plus the parent tenant rollup, so the
// wizard binds on the tighter remaining in one round-trip. Cross-tenant projectId
// is already a 404 via ResolveScope.
func (d *Deps) GetProjectQuota(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	tenantID := id.ActiveTenantID
	projectID := chi.URLParam(r, "projectId")

	snap, err := d.clusterSnapshot(r)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	tenantUsage, byProject, err := d.Store.ComputeUsage(r.Context(), tenantID, snap)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	projectQuota, err := d.getQuotaOrUnlimited(r, "project", projectID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	tenantQuota, err := d.getQuotaOrUnlimited(r, "tenant", tenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, types.ProjectQuotaResponse{
		Project: buildQuotaWithUsage("project", projectID, projectQuota, byProject[projectID]),
		Tenant:  buildQuotaWithUsage("tenant", tenantID, tenantQuota, tenantUsage),
	})
}

// PutProjectQuota serves PUT /api/tenants/{tenantId}/projects/{projectId}/quota
// (Owner): an Owner subdivides tenant capacity across projects. Each non-null
// project limit must be ≤ the corresponding tenant limit (400 otherwise);
// sum-of-projects ≤ tenant is NOT enforced (the tenant check at create is the
// backstop — ADR-0012).
func (d *Deps) PutProjectQuota(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	tenantID := id.ActiveTenantID
	projectID := chi.URLParam(r, "projectId")

	req, apiErr := decodeSetQuota(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}

	// Validate each project limit against the tenant limit (per-dimension). A nil
	// tenant limit (unlimited) accepts any project limit.
	tenantQuota, err := d.getQuotaOrUnlimited(r, "tenant", tenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if apiErr := validateProjectUnderTenant(req, tenantQuota); apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}

	quota, err := d.Store.UpsertQuota(r.Context(), store.UpsertQuotaParams{
		ScopeType: "project", ScopeID: projectID,
		MaxVCPU: req.MaxVCPU, MaxRAMMB: req.MaxRAMMB, MaxDiskGB: req.MaxDiskGB, MaxCount: req.MaxCount,
	})
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// Return the project scope with fresh usage.
	snap, err := d.clusterSnapshot(r)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	_, byProject, err := d.Store.ComputeUsage(r.Context(), tenantID, snap)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, buildQuotaWithUsage("project", projectID, quota, byProject[projectID]))
}

// PutAdminTenantQuota serves PUT /api/admin/tenants/{tenantId}/quota (Admin):
// tenant quotas are platform-admin-only — a tenant Owner may not raise their own
// cap. The tenant id comes from the path (outside ResolveTenant). This route is
// mounted under MountAdmin, which does NOT run AuditOnMutation, so the mutation
// is audited here (`tenant.quota.update`) with the same fail-closed contract: an
// admin silently loosening a tenant cap must leave a trail.
func (d *Deps) PutAdminTenantQuota(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	tenantID := chi.URLParam(r, "tenantId")
	if _, err := d.Store.GetTenantByID(r.Context(), tenantID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpserver.WriteError(w, notFound("Tenant not found."))
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	req, apiErr := decodeSetQuota(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}

	// Fail-closed intent BEFORE the upsert: an intent-insert failure refuses the
	// mutation (500), so the cap is never changed without a durable record.
	pending, err := d.auditz().Begin(r.Context(), auditz.Intent{
		Action:      "tenant.quota.update",
		ActorUserID: id.UserID,
		TenantID:    tenantID,
		TargetType:  "tenant",
		TargetID:    tenantID,
		IP:          remoteIP(r),
	})
	if err != nil {
		d.logger().Error("audit intent for tenant.quota.update failed — mutation refused", "tenant", tenantID, "err", err)
		httpserver.WriteError(w, &types.APIError{Code: "internal", Message: "internal server error", Status: http.StatusInternalServerError})
		return
	}

	quota, err := d.Store.UpsertQuota(r.Context(), store.UpsertQuotaParams{
		ScopeType: "tenant", ScopeID: tenantID,
		MaxVCPU: req.MaxVCPU, MaxRAMMB: req.MaxRAMMB, MaxDiskGB: req.MaxDiskGB, MaxCount: req.MaxCount,
	})
	if err != nil {
		finalizeErr(pending, r, err)
		httpserver.WriteError(w, err)
		return
	}
	snap, err := d.clusterSnapshot(r)
	if err != nil {
		finalizeErr(pending, r, err)
		httpserver.WriteError(w, err)
		return
	}
	usage, _, err := d.Store.ComputeUsage(r.Context(), tenantID, snap)
	if err != nil {
		finalizeErr(pending, r, err)
		httpserver.WriteError(w, err)
		return
	}
	pending.Finalize(r.Context(), "success", map[string]any{"status": http.StatusOK})
	httpserver.WriteJSON(w, http.StatusOK, buildQuotaWithUsage("tenant", tenantID, quota, usage))
}

// decodeSetQuota decodes and range-validates a SetQuotaRequest (negative limits
// are rejected as invalid_request).
func decodeSetQuota(r *http.Request) (types.SetQuotaRequest, *types.APIError) {
	var req types.SetQuotaRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return req, badRequest("Request body must be a JSON SetQuotaRequest.")
	}
	if (req.MaxVCPU != nil && *req.MaxVCPU < 0) ||
		(req.MaxRAMMB != nil && *req.MaxRAMMB < 0) ||
		(req.MaxDiskGB != nil && *req.MaxDiskGB < 0) ||
		(req.MaxCount != nil && *req.MaxCount < 0) {
		return req, badRequest("Quota limits must not be negative.")
	}
	return req, nil
}

// validateProjectUnderTenant enforces per-dimension project ≤ tenant. A nil
// tenant limit means unlimited on that dimension, so any project value passes.
func validateProjectUnderTenant(req types.SetQuotaRequest, tenant *store.Quota) *types.APIError {
	if tenant == nil {
		return nil
	}
	if req.MaxVCPU != nil && tenant.MaxVCPU != nil && *req.MaxVCPU > *tenant.MaxVCPU {
		return badRequest("Project vCPU limit exceeds the tenant vCPU limit.")
	}
	if req.MaxRAMMB != nil && tenant.MaxRAMMB != nil && *req.MaxRAMMB > *tenant.MaxRAMMB {
		return badRequest("Project RAM limit exceeds the tenant RAM limit.")
	}
	if req.MaxDiskGB != nil && tenant.MaxDiskGB != nil && *req.MaxDiskGB > *tenant.MaxDiskGB {
		return badRequest("Project disk limit exceeds the tenant disk limit.")
	}
	if req.MaxCount != nil && tenant.MaxCount != nil && *req.MaxCount > *tenant.MaxCount {
		return badRequest("Project resource count limit exceeds the tenant count limit.")
	}
	return nil
}
