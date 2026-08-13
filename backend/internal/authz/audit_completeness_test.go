package authz_test

import (
	"io"
	"log/slog"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// tenantSubtreePrefix is the tenant-scoped mutating surface AuditOnMutation must
// cover. Admin routes (/api/admin/…) are deliberately out of scope (plan §4).
const tenantSubtreePrefix = "/api/tenants/{tenantId}"

// buildAuditRouter constructs the exact production router so the completeness
// check runs against the real mount table, not a hand-maintained copy.
func buildAuditRouter(t *testing.T) chi.Routes {
	t.Helper()
	noop := func(http.ResponseWriter, *http.Request) {}
	mw := &authz.Middleware{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	api := &handlers.Deps{Authz: mw}
	deps := httpserver.Deps{
		Cfg:       &config.Config{},
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Auth:      &auth.Handler{Sessions: auth.NewSessions(nil, false, false, time.Hour, 24*time.Hour)},
		Health:    noop,
		Events:    noop,
		ConsoleWS: http.HandlerFunc(noop),
		Authz:     mw,
		Account:   api.MountAccount,
		Admin:     api.MountAdmin,
		Tenant:    api.MountTenant,
	}
	routes, ok := httpserver.New(deps).(chi.Routes)
	if !ok {
		t.Fatal("httpserver.New did not return a chi.Routes")
	}
	return routes
}

// TestAuditCompletenessTenantSubtree is the durable guardrail that mirrors the
// permission-table completeness test: every non-GET route on the tenant subtree
// (a) is wrapped by AuditOnMutation in the real router and (b) has a non-empty
// AuditAction. A mutating route added without either fails CI here — closing the
// "no mutation without an audit entry" iron rule structurally.
func TestAuditCompletenessTenantSubtree(t *testing.T) {
	// Method-value code pointers are stable across receivers, so this identifies
	// the AuditOnMutation middleware in a walked chain without executing it.
	auditPtr := reflect.ValueOf((&authz.Middleware{}).AuditOnMutation).Pointer()

	var missingWrap, missingAction []string
	err := chi.Walk(buildAuditRouter(t), func(method, pattern string, _ http.Handler, mws ...func(http.Handler) http.Handler) error {
		if method == http.MethodGet {
			return nil
		}
		if !strings.HasPrefix(pattern, tenantSubtreePrefix) {
			return nil // admin + account mutations are out of this middleware's scope
		}
		wrapped := false
		for _, mw := range mws {
			if reflect.ValueOf(mw).Pointer() == auditPtr {
				wrapped = true
				break
			}
		}
		if !wrapped {
			missingWrap = append(missingWrap, method+" "+pattern)
		}
		if authz.AuditAction(method, pattern, nil) == "" {
			missingAction = append(missingAction, method+" "+pattern)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("chi.Walk: %v", err)
	}

	if len(missingWrap) > 0 {
		sort.Strings(missingWrap)
		t.Errorf("mutating tenant routes NOT wrapped by AuditOnMutation:\n  %s", strings.Join(missingWrap, "\n  "))
	}
	if len(missingAction) > 0 {
		sort.Strings(missingAction)
		t.Errorf("mutating tenant routes with no authz.AuditAction entry (add to auditActions):\n  %s", strings.Join(missingAction, "\n  "))
	}
}

// TestAuditActionRefinesGuestVerb documents the one dynamic case: the wildcard
// lifecycle route derives its verb from the {action} path param at request time.
func TestAuditActionRefinesGuestVerb(t *testing.T) {
	pattern := "/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/{action}"
	// nil urlParam (completeness path) → base action, still non-empty.
	if got := authz.AuditAction(http.MethodPost, pattern, nil); got != "guest.action" {
		t.Fatalf("AuditAction(nil params) = %q, want guest.action", got)
	}
	// A concrete verb refines the action.
	urlParam := func(k string) string {
		if k == "action" {
			return "start"
		}
		return ""
	}
	if got := authz.AuditAction(http.MethodPost, pattern, urlParam); got != "guest.start" {
		t.Fatalf("AuditAction(action=start) = %q, want guest.start", got)
	}
	// An unmapped route → empty (drives the fail-closed 500).
	if got := authz.AuditAction(http.MethodPost, "/api/tenants/{tenantId}/nope", nil); got != "" {
		t.Fatalf("AuditAction(unmapped) = %q, want empty", got)
	}
}
