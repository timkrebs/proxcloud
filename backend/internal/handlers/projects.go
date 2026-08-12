package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/bootstrap"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// poolComment tags pools Proxcloud creates so they are recognizable in the PVE UI.
const poolComment = "managed by Proxcloud"

// ListProjects serves GET /api/tenants/{tenantId}/projects.
func (d *Deps) ListProjects(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	projs, err := d.Store.ListProjectsByTenant(r.Context(), id.ActiveTenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]types.Project, 0, len(projs))
	for i := range projs {
		out = append(out, toProject(&projs[i]))
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

// GetProject serves GET /api/tenants/{tenantId}/projects/{projectId}.
func (d *Deps) GetProject(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	proj, err := d.Store.GetProjectByID(r.Context(), chi.URLParam(r, "projectId"))
	if errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// Cross-tenant → 404 (ResolveScope already enforces this; defensive).
	if proj.TenantID != id.ActiveTenantID {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProject(proj))
}

// CreateProject serves POST /api/tenants/{tenantId}/projects (Owner). Derives a
// collision-suffixed slug + pool id pc-<tenantslug>-<projslug>, ensures the
// Proxmox pool exists, then inserts the row. 409 on a duplicate name.
func (d *Deps) CreateProject(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	var req types.CreateProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, badRequest("Request body must be JSON with a name."))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		httpserver.WriteError(w, badRequest("Project name is required."))
		return
	}

	tenant, err := d.Store.GetTenantByID(r.Context(), id.ActiveTenantID)
	if errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, notFound("Tenant not found."))
		return
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	existing, err := d.Store.ListProjectsByTenant(r.Context(), id.ActiveTenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	for i := range existing {
		if strings.EqualFold(existing[i].Name, name) {
			httpserver.WriteError(w, &types.APIError{Code: "conflict", Message: "A project with that name already exists.", Status: http.StatusConflict})
			return
		}
	}
	taken := map[string]bool{}
	for i := range existing {
		taken[existing[i].Slug] = true
	}
	slug := uniqueSlug(slugify(name), taken)
	poolID := fmt.Sprintf("pc-%s-%s", tenant.Slug, slug)

	// Create the pool before the row (ADR-0008): a project must never name a
	// pool that does not exist. Idempotent; surfaces the verbatim PVE error.
	if err := bootstrap.EnsureProjectPool(r.Context(), d.PVE, poolID, poolComment); err != nil {
		httpserver.WriteError(w, err)
		return
	}

	proj, err := d.Store.CreateProject(r.Context(), store.CreateProjectParams{
		TenantID: id.ActiveTenantID, Name: name, Slug: slug, PoolID: poolID,
	})
	if err != nil {
		// A duplicate slug within the tenant, or the globally-unique pool id
		// (migration 000002) colliding cross-tenant → 409, not a leaked 500.
		if errors.Is(err, store.ErrConflict) {
			httpserver.WriteError(w, &types.APIError{Code: "conflict", Message: "A project with that name already exists.", Status: http.StatusConflict})
			return
		}
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toProject(proj))
}

// RenameProject serves PATCH /api/tenants/{tenantId}/projects/{projectId}
// (Owner). Renames only; slug and pool id are immutable (ADR-0008).
func (d *Deps) RenameProject(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	var req types.RenameProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, badRequest("Request body must be JSON with a name."))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		httpserver.WriteError(w, badRequest("Project name is required."))
		return
	}
	pid := chi.URLParam(r, "projectId")

	// Defensive cross-tenant re-check (consistency with Get/DeleteProject):
	// resolve the row and confirm it belongs to the active tenant before the
	// blind UPDATE, so a {projectId} from another tenant is a 404, never a
	// silent cross-tenant rename.
	existing, err := d.Store.GetProjectByID(r.Context(), pid)
	if errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if existing.TenantID != id.ActiveTenantID {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}

	proj, err := d.Store.RenameProject(r.Context(), pid, name)
	if errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, toProject(proj))
}

// DeleteProject serves DELETE /api/tenants/{tenantId}/projects/{projectId}
// (Owner). Only when empty (no active/pending ownership); 409 otherwise. Deletes
// the pool, then the row. Requires confirmName to match the project name.
func (d *Deps) DeleteProject(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	pid := chi.URLParam(r, "projectId")
	var req types.DeleteProjectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, badRequest("Request body must be JSON with confirmName."))
		return
	}

	proj, err := d.Store.GetProjectByID(r.Context(), pid)
	if errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if proj.TenantID != id.ActiveTenantID {
		httpserver.WriteError(w, notFound("Project not found."))
		return
	}
	if req.ConfirmName != proj.Name {
		httpserver.WriteError(w, badRequest("Confirmation name does not match the project name."))
		return
	}

	n, err := d.Store.CountActiveOwnershipByProject(r.Context(), pid)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if n > 0 {
		httpserver.WriteError(w, &types.APIError{
			Code:    "conflict",
			Message: "Project is not empty — move or delete its resources first.",
			Status:  http.StatusConflict,
		})
		return
	}

	// Delete the pool first (never orphans a guest — we just verified emptiness),
	// then the row. A pool-delete failure surfaces the verbatim PVE error.
	if err := d.PVE.DeletePool(r.Context(), proj.PoolID); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if err := d.Store.DeleteProject(r.Context(), pid); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// uniqueSlug returns base, or base-2, base-3, … until it is not in taken.
func uniqueSlug(base string, taken map[string]bool) string {
	if !taken[base] {
		return base
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if !taken[candidate] {
			return candidate
		}
	}
}
