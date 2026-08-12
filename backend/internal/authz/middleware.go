package authz

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// Middleware carries the authz chain's dependencies. It mutates the per-request
// *auth.Identity in place (the pointer already lives in context, set by
// auth.Authenticate) so downstream handlers read ActiveTenantID / TenantRole /
// ResolvedProjectID / EffectiveRole without re-deriving scope.
//
// The chain, per tenant-scoped group:
//
//	authenticate → ResolveTenant → ResolveScope → Enforce → AuditOnMutation(stub) → handler
//
// authz imports auth+store; auth/store never import authz (one-way).
type Middleware struct {
	Store store.Store
	Log   *slog.Logger
}

func (m *Middleware) logger() *slog.Logger {
	if m.Log != nil {
		return m.Log
	}
	return slog.Default()
}

// projectRolesCtxKey carries the per-request project-scope role map from
// ResolveTenant to ResolveScope (one membership scan per request; ADR-0007).
type projectRolesCtxKey struct{}

func projectRolesFrom(ctx context.Context) map[string]string {
	if v, ok := ctx.Value(projectRolesCtxKey{}).(map[string]string); ok {
		return v
	}
	return nil
}

// ResolveTenant validates the {tenantId} path param against the caller's
// membership and seeds ActiveTenantID + TenantRole. A platform-admin passes for
// ANY tenant as effective Owner (support/impersonation, and so admins never 404
// their own default tenant). A non-member — at neither tenant nor project scope
// — gets 404, no existence leak.
func (m *Middleware) ResolveTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.IdentityFrom(r.Context())
		if !ok {
			writeErr(w, errUnauthenticated())
			return
		}
		tenantID := chi.URLParam(r, "tenantId")
		if tenantID == "" {
			m.logger().Error("authz: ResolveTenant on a route without {tenantId}", "path", r.URL.Path)
			writeErr(w, errInternal())
			return
		}

		if id.IsPlatformAdmin {
			id.ActiveTenantID = tenantID
			id.TenantRole = RoleOwnerStr
			next.ServeHTTP(w, r.WithContext(withProjectRoles(r.Context(), nil)))
			return
		}

		tenantRole, projectRoles, err := m.Store.GetEffectiveRoles(r.Context(), id.UserID, tenantID)
		if err != nil {
			m.logger().Error("authz: GetEffectiveRoles", "err", err)
			writeErr(w, errInternal())
			return
		}
		// Not a member at either scope → 404 (no existence leak).
		if tenantRole == "" && len(projectRoles) == 0 {
			writeErr(w, errNotFound())
			return
		}
		id.ActiveTenantID = tenantID
		id.TenantRole = tenantRole
		next.ServeHTTP(w, r.WithContext(withProjectRoles(r.Context(), projectRoles)))
	})
}

func withProjectRoles(ctx context.Context, roles map[string]string) context.Context {
	return context.WithValue(ctx, projectRolesCtxKey{}, roles)
}

// ResolveScope resolves the request's project and effective role:
//   - {projectId}: GetProjectByID; 404 if project.TenantID != ActiveTenantID.
//   - {vmid}:      ResolveOwnership; 404 on any miss. ResolvedProjectID from the row.
//   - neither:     tenant-level route; ResolvedProjectID "", EffectiveRole = TenantRole.
//
// EffectiveRole = max(TenantRole, projectRole) — a project role only adds
// (ADR-0007). A platform-admin is always effective Owner.
func (m *Middleware) ResolveScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.IdentityFrom(r.Context())
		if !ok {
			writeErr(w, errUnauthenticated())
			return
		}
		projectRoles := projectRolesFrom(r.Context())

		effective := func(projRole string) string {
			if id.IsPlatformAdmin {
				return RoleOwnerStr
			}
			return MaxRole(id.TenantRole, projRole)
		}

		if pid := chi.URLParam(r, "projectId"); pid != "" {
			proj, err := m.Store.GetProjectByID(r.Context(), pid)
			if errors.Is(err, store.ErrNotFound) {
				writeErr(w, errNotFound())
				return
			}
			if err != nil {
				m.logger().Error("authz: GetProjectByID", "err", err)
				writeErr(w, errInternal())
				return
			}
			if proj.TenantID != id.ActiveTenantID {
				writeErr(w, errNotFound()) // cross-tenant → 404
				return
			}
			id.ResolvedProjectID = pid
			id.EffectiveRole = effective(projectRoles[pid])
			next.ServeHTTP(w, r)
			return
		}

		if vmidStr := chi.URLParam(r, "vmid"); vmidStr != "" {
			vmid, err := strconv.Atoi(vmidStr)
			if err != nil || vmid < 1 {
				writeErr(w, errNotFound())
				return
			}
			own, err := ResolveOwnership(r.Context(), m.Store, vmid, id.ActiveTenantID)
			if errors.Is(err, ErrNotOwned) {
				writeErr(w, errNotFound())
				return
			}
			if err != nil {
				m.logger().Error("authz: ResolveOwnership", "err", err)
				writeErr(w, errInternal())
				return
			}
			id.ResolvedProjectID = own.ProjectID
			id.EffectiveRole = effective(projectRoles[own.ProjectID])
			next.ServeHTTP(w, r)
			return
		}

		// Tenant-level route.
		id.ResolvedProjectID = ""
		id.EffectiveRole = effective("")
		next.ServeHTTP(w, r)
	})
}

// Enforce compares the request's EffectiveRole against the permission-table
// minimum for the matched route pattern. An unregistered route is a 500 and a
// loud log — the completeness test prevents that shipping. A role denial inside
// a tenant the caller provably belongs to is 403 (not an existence leak).
func (m *Middleware) Enforce(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.IdentityFrom(r.Context())
		if !ok {
			writeErr(w, errUnauthenticated())
			return
		}
		pattern := chi.RouteContext(r.Context()).RoutePattern()
		rule, ok := Lookup(r.Method, pattern)
		if !ok {
			m.logger().Error("authz: route has no permission-table entry (add it to authz.registry)",
				"method", r.Method, "pattern", pattern)
			writeErr(w, errInternal())
			return
		}
		need := RuleMinRole(rule)
		have := ParseRole(id.EffectiveRole)
		if !RoleAtLeast(have, need) {
			writeErr(w, errForbidden())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AuditOnMutation is the Phase-4 audit choke-point, wired now on the mutating
// (non-GET) tenant subtree so the structural seam exists. Phase 3 is a
// pass-through that reads the actor/tenant/project from context and logs at
// debug; it does NOT write audit_log yet.
func (m *Middleware) AuditOnMutation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			if id, ok := auth.IdentityFrom(r.Context()); ok {
				m.logger().Debug("audit (stub)",
					"actor", id.UserID,
					"tenant", id.ActiveTenantID,
					"project", id.ResolvedProjectID,
					"method", r.Method,
					"pattern", chi.RouteContext(r.Context()).RoutePattern(),
				)
			}
		}
		next.ServeHTTP(w, r)
	})
}

// RequirePlatformAdmin gates the /api/admin/* surface.
func (m *Middleware) RequirePlatformAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, ok := auth.IdentityFrom(r.Context())
		if !ok {
			writeErr(w, errUnauthenticated())
			return
		}
		if !id.IsPlatformAdmin {
			writeErr(w, errForbidden())
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- error helpers (local to avoid importing httpserver, which imports authz) ---

func errUnauthenticated() *types.APIError {
	return &types.APIError{Code: "unauthenticated", Message: "Not signed in.", Status: http.StatusUnauthorized}
}
func errForbidden() *types.APIError {
	return &types.APIError{Code: "forbidden", Message: "You do not have permission to perform this action.", Status: http.StatusForbidden}
}
func errNotFound() *types.APIError {
	return &types.APIError{Code: "not_found", Message: "Not found.", Status: http.StatusNotFound}
}
func errInternal() *types.APIError {
	return &types.APIError{Code: "internal", Message: "internal server error", Status: http.StatusInternalServerError}
}

func writeErr(w http.ResponseWriter, e *types.APIError) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(types.ErrorEnvelope{Error: *e})
}
