package handlers

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// ListResourcesAdmin serves GET /api/admin/resources: the cluster-wide guest
// list (platform-admin). Owned guests carry no project enrichment here (the
// scoped tenant list does that); VMIDs without an ownership row are flagged
// Unassigned:true to feed the Phase-6 unassigned view. Optional type/pool/node/
// search filters keep the admin list navigable.
func (d *Deps) ListResourcesAdmin(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	typ := q.Get("type")
	if typ != "" && typ != "qemu" && typ != "lxc" {
		httpserver.WriteError(w, &types.APIError{
			Code:    "invalid_request",
			Message: `type must be "qemu" or "lxc"`,
			Status:  http.StatusBadRequest,
		})
		return
	}
	pool := q.Get("pool")
	node := q.Get("node")
	search := strings.ToLower(strings.TrimSpace(q.Get("search")))

	rows, err := d.PVE.ClusterResources(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	// Which VMIDs have a live ownership row (assigned vs unassigned). Nil-safe:
	// without a store, everything reads as unmarked (test convenience).
	var owned map[int]bool
	if d.Store != nil {
		owned, err = d.Store.ListActiveVMIDs(r.Context())
		if err != nil {
			httpserver.WriteError(w, err)
			return
		}
	}

	out := []types.GuestSummary{}
	for _, row := range rows {
		if row.Type != "qemu" && row.Type != "lxc" {
			continue
		}
		if typ != "" && row.Type != typ {
			continue
		}
		if pool != "" && row.Pool != pool {
			continue
		}
		if node != "" && row.Node != node {
			continue
		}
		if search != "" &&
			!strings.Contains(strings.ToLower(row.Name), search) &&
			!strings.Contains(strconv.Itoa(row.VMID), search) {
			continue
		}
		g := guestSummary(row)
		if owned != nil && !owned[g.VMID] {
			g.Unassigned = true
		}
		d.overlayTransitional(&g)
		out = append(out, g)
	}
	sortGuests(out)
	httpserver.WriteJSON(w, http.StatusOK, out)
}

// ListTenantResources serves GET /api/tenants/{tenantId}/resources: only the
// tenant's owned guests, tenant-filtered in SQL. Each GuestSummary is enriched
// with its project + creator; creators are resolved in one batch (no N+1).
func (d *Deps) ListTenantResources(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.IdentityFrom(r.Context())
	if !ok {
		httpserver.WriteError(w, &types.APIError{Code: "unauthenticated", Message: "Not signed in.", Status: http.StatusUnauthorized})
		return
	}
	if d.Store == nil {
		httpserver.WriteError(w, &types.APIError{Code: "internal", Message: "store not configured", Status: http.StatusInternalServerError})
		return
	}
	tenantID := id.ActiveTenantID

	q := r.URL.Query()
	typ := q.Get("type")
	if typ != "" && typ != "qemu" && typ != "lxc" {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: `type must be "qemu" or "lxc"`, Status: http.StatusBadRequest})
		return
	}
	projectID := q.Get("projectId")
	node := q.Get("node")
	search := strings.ToLower(strings.TrimSpace(q.Get("search")))

	// Ownership (tenant filter in SQL) → vmid -> ownership for live rows only.
	owns, err := d.Store.ListOwnershipByTenant(r.Context(), tenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	byVMID := make(map[int]store.ResourceOwnership, len(owns))
	creatorIDs := map[string]struct{}{}
	for _, o := range owns {
		if o.Status != "active" && o.Status != "pending" {
			continue
		}
		byVMID[o.VMID] = o
		if o.CreatedBy != nil && *o.CreatedBy != "" {
			creatorIDs[*o.CreatedBy] = struct{}{}
		}
	}

	// Project names for this tenant.
	projName := map[string]string{}
	if projs, err := d.Store.ListProjectsByTenant(r.Context(), tenantID); err == nil {
		for _, p := range projs {
			projName[p.ID] = p.Name
		}
	} else {
		// Best-effort enrichment: guests still list, just without project names.
		d.logger().Warn("scoped resources: project-name enrichment failed", "tenant", tenantID, "err", err)
	}

	// Creators in one batch.
	creators := map[string]store.User{}
	if len(creatorIDs) > 0 {
		ids := make([]string, 0, len(creatorIDs))
		for cid := range creatorIDs {
			ids = append(ids, cid)
		}
		if m, err := d.Store.ListUsersByIDs(r.Context(), ids); err == nil {
			creators = m
		} else {
			// Best-effort enrichment: guests still list, just without creator names.
			d.logger().Warn("scoped resources: creator enrichment failed", "tenant", tenantID, "err", err)
		}
	}

	rows, err := d.PVE.ClusterResources(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	out := []types.GuestSummary{}
	for _, row := range rows {
		if row.Type != "qemu" && row.Type != "lxc" {
			continue
		}
		own, owned := byVMID[row.VMID]
		if !owned {
			continue // not this tenant's guest
		}
		if typ != "" && row.Type != typ {
			continue
		}
		if projectID != "" && own.ProjectID != projectID {
			continue
		}
		if node != "" && row.Node != node {
			continue
		}
		if search != "" &&
			!strings.Contains(strings.ToLower(row.Name), search) &&
			!strings.Contains(strconv.Itoa(row.VMID), search) {
			continue
		}
		g := guestSummary(row)
		g.ProjectID = own.ProjectID
		g.ProjectName = projName[own.ProjectID]
		g.CreatedBy = creatorName(own.CreatedBy, creators)
		d.overlayTransitional(&g)
		out = append(out, g)
	}
	sortGuests(out)
	httpserver.WriteJSON(w, http.StatusOK, out)
}

// creatorName resolves an ownership CreatedBy id to a display name or email,
// or "" for backfilled/unknown creators.
func creatorName(createdBy *string, creators map[string]store.User) string {
	if createdBy == nil || *createdBy == "" {
		return ""
	}
	u, ok := creators[*createdBy]
	if !ok {
		return ""
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Email
}

// overlayTransitional applies the running-task status override to a guest.
func (d *Deps) overlayTransitional(g *types.GuestSummary) {
	if d.Registry == nil {
		return
	}
	if transitional, upid, ok := d.Registry.ActiveFor(g.VMID); ok {
		g.Status = transitional
		g.PendingTaskUPID = upid
	}
}

func sortGuests(out []types.GuestSummary) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].VMID != out[j].VMID {
			return out[i].VMID < out[j].VMID
		}
		return out[i].ID < out[j].ID
	})
}

// guestSummary digests one raw guest row into the wire shape: CPU fraction
// becomes percent, PVE's joined tag string becomes a slice, status passes
// through lowercased (PVE's own vocabulary is already lowercase).
func guestSummary(row proxmox.RawResource) types.GuestSummary {
	return types.GuestSummary{
		ID:        row.ID,
		Type:      row.Type,
		VMID:      row.VMID,
		Name:      row.Name,
		Node:      row.Node,
		Status:    strings.ToLower(row.Status),
		UptimeSec: row.Uptime,
		CPUPct:    row.CPU * 100,
		Cores:     row.MaxCPU,
		MemUsed:   row.Mem,
		MemMax:    row.MaxMem,
		DiskMax:   row.MaxDisk,
		Pool:      row.Pool,
		Tags:      splitPVEList(row.Tags),
		Template:  row.Template,
	}
}
