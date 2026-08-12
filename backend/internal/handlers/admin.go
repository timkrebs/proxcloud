package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/bootstrap"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// ListTenantsAdmin serves GET /api/admin/tenants (platform-admin): every tenant.
func (d *Deps) ListTenantsAdmin(w http.ResponseWriter, r *http.Request) {
	if !d.requireStore(w) {
		return
	}
	tenants, err := d.Store.ListTenants(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	out := make([]types.Tenant, 0, len(tenants))
	for i := range tenants {
		out = append(out, toTenant(&tenants[i]))
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

// CreateTenantAdmin serves POST /api/admin/tenants (platform-admin). It derives
// the slug from the name and rejects a duplicate (409). Per the lead's decision,
// it also creates a "default" project + its Proxmox pool so the tenant is usable
// immediately.
func (d *Deps) CreateTenantAdmin(w http.ResponseWriter, r *http.Request) {
	if !d.requireStore(w) {
		return
	}
	var req types.CreateTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, badRequest("Request body must be JSON with a name."))
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		httpserver.WriteError(w, badRequest("Tenant name is required."))
		return
	}
	// Derive the slug + default pool id up front so pool provisioning and the
	// row writes agree on one identity (ADR-0008).
	slug := slugify(name)
	poolID := fmt.Sprintf("pc-%s-default", slug)

	// Cheap early 409 on an obvious duplicate slug (no pool created for a request
	// we will reject). The TOCTOU race with the insert below is closed by the
	// ErrConflict mapping inside the transaction.
	if _, err := d.Store.GetTenantBySlug(r.Context(), slug); err == nil {
		httpserver.WriteError(w, &types.APIError{Code: "conflict", Message: "A tenant with that name already exists.", Status: http.StatusConflict})
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		httpserver.WriteError(w, err)
		return
	}

	// Provision the pool FIRST, before any DB write. A pool failure (e.g. the dev
	// token lacking Pool.Allocate) must leave NO tenant/project rows behind, so a
	// retry is clean — hence pool-first, then the transactional row writes. The
	// verbatim PVE error is surfaced honestly (iron rule 5).
	if err := bootstrap.EnsureProjectPool(r.Context(), d.PVE, poolID, poolComment); err != nil {
		httpserver.WriteError(w, err)
		return
	}

	// Tenant + its default project commit atomically: a project failure can never
	// orphan a committed tenant.
	var tenant *store.Tenant
	err := d.Store.WithTx(r.Context(), func(tx store.Store) error {
		t, err := tx.CreateTenant(r.Context(), store.CreateTenantParams{Name: name, Slug: slug})
		if err != nil {
			return err
		}
		tenant = t
		_, err = tx.CreateProject(r.Context(), store.CreateProjectParams{
			TenantID: t.ID, Name: "Default", Slug: "default", PoolID: poolID,
		})
		return err
	})
	if err != nil {
		if errors.Is(err, store.ErrConflict) {
			httpserver.WriteError(w, &types.APIError{Code: "conflict", Message: "A tenant with that name already exists.", Status: http.StatusConflict})
			return
		}
		d.logger().Error("create tenant", "slug", slug, "err", err)
		httpserver.WriteError(w, &types.APIError{Code: "internal", Message: "Failed to create the tenant.", Status: http.StatusInternalServerError})
		return
	}
	httpserver.WriteJSON(w, http.StatusCreated, toTenant(tenant))
}
