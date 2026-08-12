package handlers

import (
	"net/http"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// ListMembers serves GET /api/tenants/{tenantId}/members (Owner). It returns
// every membership scoped to the tenant itself plus every membership scoped to
// one of its projects (tenant-filtered in SQL). Read-only in Phase 3 —
// invitations and role edits land in Phase 5.
func (d *Deps) ListMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := requireIdentity(w, r)
	if !ok || !d.requireStore(w) {
		return
	}
	tenantID := id.ActiveTenantID

	mems, err := d.Store.ListMembershipsByScope(r.Context(), "tenant", tenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	projs, err := d.Store.ListProjectsByTenant(r.Context(), tenantID)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// All project-scope grants in ONE query (no per-project N+1).
	projIDs := make([]string, 0, len(projs))
	for i := range projs {
		projIDs = append(projIDs, projs[i].ID)
	}
	pms, err := d.Store.ListMembershipsByScopes(r.Context(), "project", projIDs)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	mems = append(mems, pms...)

	// Resolve user identities in one batch (no N+1).
	ids := make([]string, 0, len(mems))
	for i := range mems {
		ids = append(ids, mems[i].UserID)
	}
	users := map[string]store.User{}
	if u, err := d.Store.ListUsersByIDs(r.Context(), ids); err == nil {
		users = u
	} else {
		// Best-effort enrichment: a lookup failure degrades to bare ids rather
		// than failing the list, but is logged so it is diagnosable.
		d.logger().Warn("list members: resolve user identities failed (returning bare ids)", "err", err)
	}

	out := make([]types.Member, 0, len(mems))
	for i := range mems {
		m := mems[i]
		u := users[m.UserID]
		out = append(out, types.Member{
			UserID:      m.UserID,
			Email:       u.Email,
			DisplayName: u.DisplayName,
			ScopeType:   m.ScopeType,
			ScopeID:     m.ScopeID,
			Role:        m.Role,
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}
