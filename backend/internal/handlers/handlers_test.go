package handlers_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
)

// mountLegacy mounts the PVE-facing handlers at the flat v1 paths so the
// existing handler unit tests exercise handler logic directly, decoupled from
// the production tenant/admin route tree (whose correctness is covered by the
// authz completeness test against the real router). ListResources moved to the
// admin variant; everything else is method-for-method the old table minus the
// routes that now require an Identity/Store (create, deployment, tenant scope).
func mountLegacy(d *handlers.Deps, r chi.Router) {
	r.Get("/cluster", d.GetCluster)
	r.Get("/cluster/nextid", d.GetNextID)

	r.Get("/nodes", d.ListNodes)
	r.Get("/nodes/{node}", d.GetNode)
	r.Get("/nodes/{node}/metrics", d.GetNodeMetrics)
	r.Get("/nodes/{node}/bridges", d.GetNodeBridges)
	r.Get("/nodes/{node}/storages", d.GetNodeStorages)
	r.Get("/nodes/{node}/storages/{storage}/content", d.GetStorageContent)

	r.Get("/resources", d.ListResourcesAdmin)
	r.Get("/pools", d.ListPools)
	r.Get("/storage", d.ListStorage)

	r.Get("/guests/{node}/{type}/{vmid}", d.GetGuest)
	r.Patch("/guests/{node}/{type}/{vmid}/config", d.UpdateGuestConfig)
	r.Get("/guests/{node}/{type}/{vmid}/metrics", d.GetGuestMetrics)
	r.Get("/guests/{node}/{type}/{vmid}/interfaces", d.GetGuestInterfaces)
	r.Post("/guests/{node}/{type}/{vmid}/resize", d.ResizeGuestDisk)
	r.Get("/guests/{node}/{type}/{vmid}/snapshots", d.ListSnapshots)
	r.Post("/guests/{node}/{type}/{vmid}/snapshots", d.CreateSnapshot)
	r.Post("/guests/{node}/{type}/{vmid}/snapshots/{name}/rollback", d.RollbackSnapshot)
	r.Delete("/guests/{node}/{type}/{vmid}/snapshots/{name}", d.DeleteSnapshot)
	r.Get("/guests/{node}/{type}/{vmid}/firewall", d.GetGuestFirewall)
	r.Put("/guests/{node}/{type}/{vmid}/firewall/options", d.SetGuestFirewall)
	r.Get("/guests/{node}/{type}/{vmid}/acl", d.GetGuestACL)
	r.Post("/guests/{node}/{type}/{vmid}/{action}", d.GuestAction)
	r.Delete("/guests/{node}/{type}/{vmid}", d.DeleteGuest)

	r.Get("/tasks", d.ListTasks)
	r.Get("/tasks/{upid}", d.GetTask)
	r.Get("/tasks/{upid}/log", d.GetTaskLog)

	r.Get("/notifications", d.ListNotifications)
	r.Post("/notifications/read", d.MarkNotificationsRead)
	r.Get("/pricing", d.GetPricing)
}

// doGet mounts the handlers on a fresh chi router (as main.go does under
// /api) and performs one GET against it.
func doGet(t *testing.T, mock *proxmoxtest.MockClient, target string) *httptest.ResponseRecorder {
	t.Helper()
	d := &handlers.Deps{
		PVE: mock,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) { mountLegacy(d, r) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// decodeBody decodes the recorded JSON body into T, failing the test with
// the raw body on mismatch.
func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode response: %v (body %q)", err, rec.Body.String())
	}
	return v
}

// wantErrorEnvelope asserts status code and the stable error code in the
// standard envelope.
func wantErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, wantStatus, rec.Body.String())
	}
	env := decodeBody[types.ErrorEnvelope](t, rec)
	if env.Error.Code != wantCode {
		t.Fatalf("error code = %q, want %q (body %q)", env.Error.Code, wantCode, rec.Body.String())
	}
}

// pveUnreachable is the error the real client returns when the Proxmox API
// cannot be reached, exactly as the error mapper produces it.
func pveUnreachable() *types.APIError {
	return &types.APIError{
		Code:       "proxmox_unreachable",
		Message:    "query: cannot reach the Proxmox API",
		PVEMessage: "connection refused",
		Status:     http.StatusBadGateway,
	}
}
