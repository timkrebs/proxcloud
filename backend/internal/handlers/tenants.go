package handlers

import (
	"errors"
	"net/http"
	"regexp"
	"strings"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// requireIdentity returns the authenticated principal or writes 401 and false.
func requireIdentity(w http.ResponseWriter, r *http.Request) (*auth.Identity, bool) {
	id, ok := auth.IdentityFrom(r.Context())
	if !ok {
		httpserver.WriteError(w, &types.APIError{Code: "unauthenticated", Message: "Not signed in.", Status: http.StatusUnauthorized})
		return nil, false
	}
	return id, true
}

// requireStore guards handlers that need the Postgres system of record.
func (d *Deps) requireStore(w http.ResponseWriter) bool {
	if d.Store == nil {
		httpserver.WriteError(w, &types.APIError{Code: "internal", Message: "store not configured", Status: http.StatusInternalServerError})
		return false
	}
	return true
}

// GetTenantSummary serves GET /api/tenants/{tenantId}/summary.
func (d *Deps) GetTenantSummary(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
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
	projs, err := d.Store.ListProjectsByTenant(r.Context(), id.ActiveTenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	owns, err := d.Store.ListOwnershipByTenant(r.Context(), id.ActiveTenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	resourceCount := 0
	for _, o := range owns {
		if o.Status == "active" || o.Status == "pending" {
			resourceCount++
		}
	}
	httpserver.WriteJSON(w, http.StatusOK, types.TenantSummary{
		Tenant:        toTenant(tenant),
		Role:          id.EffectiveRole,
		ProjectCount:  len(projs),
		ResourceCount: resourceCount,
	})
}

// --- conversions ---

func toTenant(t *store.Tenant) types.Tenant {
	return types.Tenant{ID: t.ID, Name: t.Name, Slug: t.Slug, CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt}
}

func toProject(p *store.Project) types.Project {
	return types.Project{
		ID: p.ID, TenantID: p.TenantID, Name: p.Name, Slug: p.Slug,
		PoolID: p.PoolID, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt,
	}
}

// --- shared error + slug helpers ---

func notFound(msg string) *types.APIError {
	return &types.APIError{Code: "not_found", Message: msg, Status: http.StatusNotFound}
}

func badRequest(msg string) *types.APIError {
	return &types.APIError{Code: "invalid_request", Message: msg, Status: http.StatusBadRequest}
}

var slugStripRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify derives a Proxmox-safe slug from a display name: lowercase, non-alnum
// runs collapsed to single hyphens, trimmed. Empty input yields "x" so a slug is
// never blank (pool ids must be valid PVE identifiers).
func slugify(name string) string {
	s := slugStripRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "x"
	}
	return s
}
