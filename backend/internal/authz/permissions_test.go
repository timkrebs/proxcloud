package authz_test

import (
	"io"
	"log/slog"
	"net/http"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// buildRealRouter constructs the exact production router (httpserver.New with
// the real Mount table plus the public + streaming routes) so the completeness
// check runs against reality, not a hand-maintained copy.
func buildRealRouter(t *testing.T) chi.Routes {
	t.Helper()
	noop := func(http.ResponseWriter, *http.Request) {}
	deps := httpserver.Deps{
		Cfg:       &config.Config{},
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Auth:      &auth.Handler{Sessions: auth.NewSessions([]byte("test-session-secret-0123456789abcd"), false)},
		Health:    noop,
		Events:    noop,                     // mounts GET /api/events
		ConsoleWS: http.HandlerFunc(noop),   // mounts GET /api/console/ws/{sessionId}
		Protected: (&handlers.Deps{}).Mount, // the real route table
	}
	h := httpserver.New(deps)
	routes, ok := h.(chi.Routes)
	if !ok {
		t.Fatalf("httpserver.New did not return a chi.Routes (got %T)", h)
	}
	return routes
}

type route struct{ method, pattern string }

func walk(t *testing.T, routes chi.Routes) []route {
	t.Helper()
	var got []route
	err := chi.Walk(routes, func(method, pattern string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		got = append(got, route{method, pattern})
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}
	return got
}

// TestEveryMountedRouteHasPermissionEntry is the durable guardrail: it fails if
// any mounted (method, pattern) is missing from the authz registry.
func TestEveryMountedRouteHasPermissionEntry(t *testing.T) {
	var missing []string
	for _, r := range walk(t, buildRealRouter(t)) {
		if _, ok := authz.Lookup(r.method, r.pattern); !ok {
			missing = append(missing, r.method+" "+r.pattern)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("routes mounted without a permission-table entry (add them to authz.registry):\n  %s",
			joinLines(missing))
	}
}

// TestNoStalePermissionEntries fails if the registry names a route that is no
// longer mounted, keeping the table honest as routes are removed/renamed.
func TestNoStalePermissionEntries(t *testing.T) {
	mounted := map[route]bool{}
	for _, r := range walk(t, buildRealRouter(t)) {
		mounted[r] = true
	}
	var stale []string
	for _, p := range authz.Registered() {
		if !mounted[route{p.Method, p.Pattern}] {
			stale = append(stale, p.Method+" "+p.Pattern)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Fatalf("permission-table entries for routes that no longer exist (remove them from authz.registry):\n  %s",
			joinLines(stale))
	}
}

func joinLines(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += "\n  "
		}
		out += s
	}
	return out
}
