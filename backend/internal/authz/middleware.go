package authz

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

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

// AuditOnMutation is the Phase-4 audit choke-point on the mutating (non-GET)
// tenant subtree: it guarantees a durable audit_log row for every mutation, or a
// 500 with nothing mutated (ADR-0012 §3). GET passes straight through (the
// self-gate that makes this a single r.Use on the whole tenant group).
//
// Fail-closed, one row: it inserts an intent row (outcome "pending") BEFORE the
// handler — a failed insert 500s and RETURNS without calling next, so nothing is
// mutated; it then runs the handler behind a status-capturing wrapper and
// finalizes the same row's outcome/detail AFTER — a failed finalize logs loudly
// but does NOT 500, because the intent row is already a durable record (no
// unlogged mutation). InsertAuditIntent + FinalizeAudit are the only audit
// mutations, so who/what/when stays immutable.
func (m *Middleware) AuditOnMutation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			next.ServeHTTP(w, r)
			return
		}
		id, ok := auth.IdentityFrom(r.Context())
		if !ok {
			writeErr(w, errUnauthenticated())
			return
		}
		if m.Store == nil {
			m.logger().Error("authz: AuditOnMutation has no store (fail-closed)")
			writeErr(w, errInternal())
			return
		}

		pattern := chi.RouteContext(r.Context()).RoutePattern()
		urlParam := func(k string) string { return chi.URLParam(r, k) }
		action := AuditAction(r.Method, pattern, urlParam)
		if action == "" {
			// A mutating route with no action-map entry: the completeness test
			// prevents this shipping, but at runtime it is fail-closed.
			m.logger().Error("authz: mutating route has no audit-action entry (add it to authz.auditActions)",
				"method", r.Method, "pattern", pattern)
			writeErr(w, errInternal())
			return
		}
		targetType, targetID := auditTarget(urlParam)

		intent := store.AuditIntent{
			ActorUserID: nonEmpty(id.UserID),
			TenantID:    nonEmpty(id.ActiveTenantID),
			ProjectID:   nonEmpty(id.ResolvedProjectID),
			Action:      action,
			TargetType:  targetType,
			TargetID:    targetID,
			IP:          clientIP(r),
		}
		auditID, err := m.Store.InsertAuditIntent(r.Context(), intent)
		if err != nil {
			// True fail-closed: the mutation never runs, so there is nothing to log.
			m.logger().Error("authz: audit intent insert failed — mutation refused",
				"action", action, "pattern", pattern, "err", err)
			writeErr(w, errInternal())
			return
		}

		// Run the handler behind a status-capturing wrapper, with an annotation
		// sink on the context so a handler (e.g. CreateGuest) may add vmid/name to
		// the audit detail — best-effort, never load-bearing for the one row.
		ann := newAnnotations()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r.WithContext(withAnnotations(r.Context(), ann)))

		outcome := outcomeForStatus(ww.Status())
		detail := ann.detail(ww.Status())
		// Detach from request cancellation so a client disconnect can never leave a
		// mutation's outcome unrecorded; the intent row is already durable regardless.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditFinalizeTimeout)
		defer cancel()
		if err := m.Store.FinalizeAudit(fctx, auditID, outcome, detail); err != nil {
			// The intent row remains durable ("pending") — a logged, non-silent gap.
			m.logger().Error("authz: audit finalize failed (intent row is durable; not 500ing)",
				"audit_id", auditID, "outcome", outcome, "err", err)
		}
	})
}

// auditFinalizeTimeout bounds the post-handler outcome write.
const auditFinalizeTimeout = 5 * time.Second

// outcomeForStatus maps an HTTP status to the audit outcome vocabulary: 2xx (and
// an unwritten 0, which net/http renders as 200) is success, 4xx is a denied
// attempt, everything else (5xx) is an error. Every attempt is recorded — a
// denied or errored mutation is still an audit event.
func outcomeForStatus(status int) string {
	switch {
	case status == 0 || (status >= 200 && status < 300):
		return "success"
	case status >= 400 && status < 500:
		return "denied"
	default:
		return "error"
	}
}

// clientIP extracts the caller's IP (middleware.RealIP has already normalized
// r.RemoteAddr) for the audit row, or nil when unavailable.
func clientIP(r *http.Request) *string {
	addr := r.RemoteAddr
	if host, _, err := net.SplitHostPort(addr); err == nil {
		addr = host
	}
	if addr == "" {
		return nil
	}
	return &addr
}

// nonEmpty returns &s when s is non-empty, else nil (for the nullable audit
// actor/tenant/project columns).
func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// --- audit detail annotation hook ---

type annotationsCtxKey struct{}

// annotations is a per-request sink the audit middleware attaches so handlers can
// enrich the outcome detail (e.g. the resolved vmid). Mutex-guarded, though in
// practice a handler writes it synchronously before returning.
type annotations struct {
	mu sync.Mutex
	kv map[string]string
}

func newAnnotations() *annotations { return &annotations{kv: map[string]string{}} }

func withAnnotations(ctx context.Context, a *annotations) context.Context {
	return context.WithValue(ctx, annotationsCtxKey{}, a)
}

// Annotate adds a key/value to the current request's audit detail, if the audit
// choke-point is active on this request (a no-op otherwise). It is best-effort
// enrichment and never affects the one-row guarantee.
func Annotate(ctx context.Context, key, value string) {
	if a, ok := ctx.Value(annotationsCtxKey{}).(*annotations); ok {
		a.mu.Lock()
		a.kv[key] = value
		a.mu.Unlock()
	}
}

// detail marshals the audit row's jsonb detail: the captured HTTP status plus any
// handler annotations. A marshal error yields nil (the row still finalizes).
func (a *annotations) detail(status int) []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	m := make(map[string]any, len(a.kv)+1)
	m["status"] = status
	for k, v := range a.kv {
		m[k] = v
	}
	b, err := json.Marshal(m)
	if err != nil {
		return nil
	}
	return b
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
