// Package httpserver wires the chi router, shared middleware, and
// response helpers for the Proxcloud API.
package httpserver

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
	"github.com/timkrebs9/proxcloud/backend/internal/version"
)

// Deps carries everything the router mounts. Fields are added per feature
// milestone; nil features simply aren't mounted.
type Deps struct {
	Cfg  *config.Config
	Log  *slog.Logger
	Auth *auth.Handler

	// Health, when set, replaces the bare liveness handler for the public
	// /api/health route (e.g. to add Proxmox reachability).
	Health http.HandlerFunc

	// Events, when set, serves GET /api/events (SSE). It is authenticated
	// but exempt from the request-timeout middleware — a stream must outlive
	// the 15s deadline.
	Events http.HandlerFunc

	// ConsoleWS, when set, serves GET /api/console/ws/{sessionId}. It is
	// authenticated by the one-shot unguessable session id (single use,
	// 25s TTL) instead of the cookie: the browser connects to the backend
	// origin directly because Next rewrites cannot proxy websockets.
	ConsoleWS http.Handler

	// Authz is the tenancy enforcement chain. When set, the /admin and
	// /tenants/{tenantId} sub-surfaces are mounted behind it; when nil, only the
	// flat account surface is served (bootstrap/degraded).
	Authz *authz.Middleware

	// Account mounts the tenant-agnostic, per-user handler routes
	// (notifications, pricing) inside the authenticated group.
	Account func(r chi.Router)

	// Admin mounts the platform-admin routes under /api/admin, behind
	// RequirePlatformAdmin.
	Admin func(r chi.Router)

	// Tenant mounts the tenant-scoped routes under /api/tenants/{tenantId},
	// behind ResolveTenant → ResolveScope → Enforce (+ AuditOnMutation on the
	// mutating subtree, applied inside Tenant).
	Tenant func(r chi.Router)
}

// New builds the chi router with the standard middleware stack.
func New(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	// Proxcloud always runs behind a single trusted reverse proxy (Caddy, ADR-0015),
	// itself behind a Cloudflare Tunnel — the backend is never directly reachable, so
	// trusting the proxy's X-Forwarded-For to recover the real client IP (rate-limit
	// keys + audit provenance) is correct here. SA1019 flags RealIP because it is
	// unsafe when directly internet-exposed, which is not our topology. A trusted-
	// proxy allowlist is the documented hardening path (security review notes).
	//lint:ignore SA1019 safe behind the single trusted proxy described above
	r.Use(middleware.RealIP)
	r.Use(accessLog(d.Log))
	r.Use(middleware.Recoverer)
	r.Use(originCheck(d.Cfg))
	r.Use(limitBody(maxRequestBodyBytes, "/api/events", "/api/console/ws/"))
	r.Use(timeoutExcept(15*time.Second, "/api/events", "/api/console/ws/"))

	r.Route("/api", func(r chi.Router) {
		health := d.Health
		if health == nil {
			health = func(w http.ResponseWriter, _ *http.Request) {
				WriteJSON(w, http.StatusOK, types.Health{Status: "ok"})
			}
		}
		r.Get("/health", health)

		// Public build metadata (WS3): the running binary's git commit, semver,
		// and build time, injected at link time via -ldflags. No auth, no secrets
		// — the CD health-check/smoke test and the frontend footer read it without
		// a session. Versioned path (/api/v1) alongside the flat routes.
		r.Get("/v1/version", versionHandler)

		// Public auth surface: first-run status/bootstrap, login, and the invite
		// accept flow (the opaque token is the credential — the caller may be signed
		// out or not yet exist). Everything else self-identifies via the session and
		// lives in the group below.
		r.Get("/auth/bootstrap-status", d.Auth.BootstrapStatus)
		r.Post("/auth/bootstrap", d.Auth.Bootstrap)
		r.Post("/auth/login", d.Auth.Login)
		// Second-factor login: the interim proxcloud_totp challenge cookie is the
		// credential (the caller is not yet signed in), so this is public.
		r.Post("/auth/login/totp", d.Auth.LoginTOTP)
		r.Get("/auth/invitations/{token}", d.Auth.ValidateInvite)
		r.Post("/auth/invitations/{token}/accept", d.Auth.AcceptInvite)

		if d.ConsoleWS != nil {
			r.Get("/console/ws/{sessionId}", d.ConsoleWS.ServeHTTP)
		}

		// Authenticated group: Authenticate injects the *Identity into context
		// and replaces the old stateless RequireSession. It splits into three
		// sub-surfaces: a flat tenant-agnostic account/stream surface, the
		// platform-admin surface (/admin), and the tenant-scoped surface
		// (/tenants/{tenantId}) behind the authz chain.
		r.Group(func(r chi.Router) {
			r.Use(d.Auth.Authenticate)

			// --- flat account + stream surface ---
			r.Post("/auth/logout", d.Auth.Logout)
			r.Get("/auth/me", d.Auth.Me)
			r.Patch("/auth/active-tenant", d.Auth.SetActiveTenant)
			r.Post("/auth/password", d.Auth.ChangePassword)
			r.Get("/auth/sessions", d.Auth.ListSessions)
			r.Delete("/auth/sessions/{id}", d.Auth.DeleteSession)

			// --- account-level TOTP + recovery management (ADR-0013 §3) ---
			r.Post("/auth/totp/enroll", d.Auth.EnrollTOTP)
			r.Post("/auth/totp/verify", d.Auth.VerifyEnrollTOTP)
			r.Post("/auth/totp/disable", d.Auth.DisableTOTP)
			r.Post("/auth/totp/recovery-codes", d.Auth.RegenerateRecoveryCodes)

			if d.Events != nil {
				r.Get("/events", d.Events)
			}
			if d.Account != nil {
				d.Account(r)
			}

			// --- platform-admin surface ---
			if d.Admin != nil && d.Authz != nil {
				r.Route("/admin", func(r chi.Router) {
					r.Use(d.Authz.RequirePlatformAdmin)
					d.Admin(r)
				})
			}

			// --- tenant-scoped surface ---
			//
			// This MUST be an inline r.Group, not r.Route("/tenants/{tenantId}"):
			// on a MOUNTED subrouter chi runs the group middleware BEFORE routing
			// the remaining path, so Enforce would see the collapsed pattern
			// "/api/tenants/{tenantId}/*" and fail every registry lookup. An inline
			// group shares the parent tree, so the chain runs at the fully-resolved
			// endpoint — Enforce sees "/api/tenants/{tenantId}/resources" etc.
			// MountTenant therefore prefixes every route with /tenants/{tenantId}.
			if d.Tenant != nil && d.Authz != nil {
				r.Group(func(r chi.Router) {
					r.Use(d.Authz.ResolveTenant)
					r.Use(d.Authz.ResolveScope)
					r.Use(d.Authz.Enforce)
					// Audit choke-point on the mutating (non-GET) subtree; the
					// middleware self-gates to non-GET so it is a single Use.
					r.Use(d.Authz.AuditOnMutation)
					d.Tenant(r)
				})
			}
		})
	})

	return r
}

// versionHandler serves GET /api/v1/version: the running binary's build
// metadata from the version package (link-time -ldflags). It needs no
// dependencies and touches no secrets, so it is mounted directly in the public
// group.
func versionHandler(w http.ResponseWriter, _ *http.Request) {
	info := version.Info()
	WriteJSON(w, http.StatusOK, types.VersionInfo{
		Commit:    info.Commit,
		Semver:    info.Semver,
		BuildTime: info.BuildTime,
	})
}

// originCheck rejects state-changing requests whose Origin (or, absent
// that, Referer) does not match the frontend. Browsers always attach
// Origin to cross-origin fetch/form POSTs, so this blocks CSRF even if a
// SameSite regression slips in; header-less non-browser clients (curl)
// pass through — they already need the session cookie.
func originCheck(cfg *config.Config) func(http.Handler) http.Handler {
	allowed := func(value string) bool {
		u, err := url.Parse(value)
		if err != nil || u.Host == "" {
			return false
		}
		origin := u.Scheme + "://" + u.Host
		if cfg != nil && cfg.FrontendOrigin != "" {
			return strings.EqualFold(origin, cfg.FrontendOrigin)
		}
		host := u.Hostname()
		return host == "localhost" || host == "127.0.0.1"
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			check := r.Header.Get("Origin")
			if check == "" {
				check = r.Header.Get("Referer")
			}
			if check != "" && !allowed(check) {
				WriteError(w, &types.APIError{
					Code:    "forbidden",
					Message: "Cross-origin request rejected.",
					Status:  http.StatusForbidden,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// maxRequestBodyBytes caps the request body the API reads on non-streaming
// routes. Wrapping the body in http.MaxBytesReader before any handler decodes it
// stops an unauthenticated attacker from OOM-ing the backend with a huge body on
// a public endpoint (login/bootstrap/invite/TOTP) — the decode/hash never sees
// more than the cap. JSON payloads here are tiny; 256 KiB is generous.
const maxRequestBodyBytes = 256 << 10

// limitBody rejects an over-declared body up front with 413 and otherwise caps
// the readable body at maxBytes. The exempt paths are the streaming endpoints
// (SSE, console WS), which carry no fixed request body.
func limitBody(maxBytes int64, exempt ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range exempt {
				if r.URL.Path == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(r.URL.Path, p)) {
					next.ServeHTTP(w, r)
					return
				}
			}
			if r.ContentLength > maxBytes {
				WriteError(w, &types.APIError{
					Code:    "request_too_large",
					Message: "Request body too large.",
					Status:  http.StatusRequestEntityTooLarge,
				})
				return
			}
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// timeoutExcept applies the standard request timeout to every route except
// the exempt paths (streaming endpoints that must outlive the deadline).
func timeoutExcept(d time.Duration, exempt ...string) func(http.Handler) http.Handler {
	timeout := middleware.Timeout(d)
	return func(next http.Handler) http.Handler {
		timed := timeout(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range exempt {
				if r.URL.Path == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(r.URL.Path, p)) {
					next.ServeHTTP(w, r)
					return
				}
			}
			timed.ServeHTTP(w, r)
		})
	}
}

// accessLog emits one structured line per request.
func accessLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.Info("http",
				"method", r.Method,
				"path", redactPath(r.URL.Path),
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur_ms", time.Since(start).Milliseconds(),
				"reqid", middleware.GetReqID(r.Context()),
			)
		})
	}
}

// inviteRoutePrefix is the public invite surface whose next path segment is the
// single-use invite token (a credential).
const inviteRoutePrefix = "/api/auth/invitations/"

// redactPath strips single-use credentials that ride in the URL path out of the
// access log. Two surfaces carry a secret as a path segment:
//   - the console one-shot session id (/api/console/ws/{sessionId}), and
//   - the invite token on the public invite routes
//     (/api/auth/invitations/{token} and .../{token}/accept).
//
// The invite token is a real credential (accepting it proves mailbox control and
// mints a session), so per ADR-0013 §5.1 it must never reach structured/access
// logs. We redact only the token segment, preserving any trailing subpath (e.g.
// /accept) so the logged route stays meaningful.
func redactPath(path string) string {
	if strings.HasPrefix(path, "/api/console/ws/") {
		return "/api/console/ws/[redacted]" // the id is a one-shot credential
	}
	if strings.HasPrefix(path, inviteRoutePrefix) {
		rest := path[len(inviteRoutePrefix):]
		if rest == "" {
			return path // /api/auth/invitations/ with no token — nothing to redact
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			// "<token>/accept" (or deeper) — redact the token, keep the tail.
			return inviteRoutePrefix + "[redacted]" + rest[i:]
		}
		return inviteRoutePrefix + "[redacted]" // "<token>" — the validate route
	}
	return path
}
