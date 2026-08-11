// Package httpserver wires the chi router, shared middleware, and
// response helpers for the Proxcloud API.
package httpserver

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
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

	// Protected mounts additional authenticated routes; set per milestone.
	Protected func(r chi.Router)
}

// New builds the chi router with the standard middleware stack.
func New(d Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(accessLog(d.Log))
	r.Use(middleware.Recoverer)
	r.Use(timeoutExcept(15*time.Second, "/api/events", "/api/console/ws/"))

	r.Route("/api", func(r chi.Router) {
		health := d.Health
		if health == nil {
			health = func(w http.ResponseWriter, _ *http.Request) {
				WriteJSON(w, http.StatusOK, types.Health{Status: "ok"})
			}
		}
		r.Get("/health", health)

		r.Post("/auth/login", d.Auth.Login)
		r.Post("/auth/logout", d.Auth.Logout)
		r.Get("/auth/me", d.Auth.Me)

		if d.ConsoleWS != nil {
			r.Get("/console/ws/{sessionId}", d.ConsoleWS.ServeHTTP)
		}

		if d.Protected != nil || d.Events != nil {
			r.Group(func(r chi.Router) {
				r.Use(d.Auth.RequireSession)
				if d.Events != nil {
					r.Get("/events", d.Events)
				}
				if d.Protected != nil {
					d.Protected(r)
				}
			})
		}
	})

	return r
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
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"dur_ms", time.Since(start).Milliseconds(),
				"reqid", middleware.GetReqID(r.Context()),
			)
		})
	}
}
