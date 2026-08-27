package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	pmx "github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

// ttlHarness wires a store-backed Deps with a PVE mock (used for the delete
// typed-confirmation name check) and a pre-seeded owned guest.
func ttlHarness(t *testing.T) (*handlers.Deps, *storetest.Fake, string) {
	t.Helper()
	f := storetest.New()
	tid := f.AddTenant("Acme", "acme")
	pid := f.AddProject(tid, "Web", "web", "pc-acme-web")
	f.AddOwnership(tid, pid, 101, "qemu", "pve01", "active", nil)
	mock := &proxmoxtest.MockClient{
		OnGuestStatus: func(_ context.Context, _ pmx.GuestRef) (*pmx.GuestStatusInfo, error) {
			return &pmx.GuestStatusInfo{Status: "stopped", Name: "web-01"}, nil
		},
	}
	d := &handlers.Deps{Store: f, PVE: mock, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return d, f, tid
}

func putTTL(d *handlers.Deps, tenantID, body string) *httptest.ResponseRecorder {
	id := &auth.Identity{UserID: "u1", Email: "u@x.io", ActiveTenantID: tenantID, EffectiveRole: "contributor"}
	r := chi.NewRouter()
	r.Put("/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/ttl", func(w http.ResponseWriter, req *http.Request) {
		req = req.WithContext(auth.ContextWithIdentity(req.Context(), id))
		d.PutGuestTTL(w, req)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/tenants/"+tenantID+"/guests/pve01/qemu/101/ttl", strings.NewReader(body))
	r.ServeHTTP(rec, req)
	return rec
}

func TestPutGuestTTLValidation(t *testing.T) {
	d, _, tid := ttlHarness(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{"valid stop", `{"action":"stop","ttlSeconds":3600}`, http.StatusOK},
		{"valid delete with confirm", `{"action":"delete","ttlSeconds":3600,"confirmName":"web-01"}`, http.StatusOK},
		{"bad action", `{"action":"pause","ttlSeconds":3600}`, http.StatusBadRequest},
		{"zero ttl", `{"action":"stop","ttlSeconds":0}`, http.StatusBadRequest},
		{"negative ttl", `{"action":"stop","ttlSeconds":-5}`, http.StatusBadRequest},
		{"over max ttl", `{"action":"stop","ttlSeconds":2592001}`, http.StatusBadRequest}, // > 30d default
		{"delete without confirm", `{"action":"delete","ttlSeconds":3600}`, http.StatusBadRequest},
		{"delete wrong confirm", `{"action":"delete","ttlSeconds":3600,"confirmName":"nope"}`, http.StatusBadRequest},
		{"malformed json", `{`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := putTTL(d, tid, tt.body)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestPutGuestTTLPersists(t *testing.T) {
	d, f, tid := ttlHarness(t)
	rec := putTTL(d, tid, `{"action":"stop","ttlSeconds":7200}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	ttl, err := f.GetTTL(context.Background(), 101)
	if err != nil {
		t.Fatalf("GetTTL: %v", err)
	}
	if ttl.Action != "stop" || ttl.OriginalDuration != 2*time.Hour {
		t.Fatalf("persisted ttl wrong: %+v", ttl)
	}
	// Feature disabled in this harness → no jobs materialized.
	if n := len(f.AllJobs()); n != 0 {
		t.Fatalf("materialized %d jobs with the feature disabled, want 0", n)
	}
}

func TestPutGuestTTLRespectsProjectMax(t *testing.T) {
	d, f, tid := ttlHarness(t)
	pid, _ := f.GetProjectByPoolID(context.Background(), "pc-acme-web")
	if _, err := f.UpsertProjectTTLPolicy(context.Background(), store.UpsertProjectTTLPolicyParams{
		TenantID: tid, ProjectID: pid.ID, MaxTTL: time.Hour,
	}); err != nil {
		t.Fatalf("seed policy: %v", err)
	}
	// 2h exceeds the 1h project max.
	rec := putTTL(d, tid, `{"action":"stop","ttlSeconds":7200}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for over-max TTL (body %s)", rec.Code, rec.Body.String())
	}
}

func TestDeleteGuestTTL(t *testing.T) {
	d, f, tid := ttlHarness(t)
	if _, err := f.UpsertTTL(context.Background(), store.UpsertTTLParams{
		TenantID: tid, ProjectID: mustPID(t, f), VMID: 101, Action: "stop",
		ExpiresAt: time.Now().Add(time.Hour), OriginalDuration: time.Hour,
	}); err != nil {
		t.Fatalf("seed ttl: %v", err)
	}
	id := &auth.Identity{UserID: "u1", ActiveTenantID: tid, EffectiveRole: "contributor"}
	r := chi.NewRouter()
	r.Delete("/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/ttl", func(w http.ResponseWriter, req *http.Request) {
		req = req.WithContext(auth.ContextWithIdentity(req.Context(), id))
		d.DeleteGuestTTL(w, req)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/api/tenants/"+tid+"/guests/pve01/qemu/101/ttl", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if _, err := f.GetTTL(context.Background(), 101); err == nil {
		t.Fatal("TTL still present after DELETE")
	}
}

func TestExtendGuestTTL(t *testing.T) {
	d, f, tid := ttlHarness(t)
	orig := time.Now().Add(time.Hour)
	if _, err := f.UpsertTTL(context.Background(), store.UpsertTTLParams{
		TenantID: tid, ProjectID: mustPID(t, f), VMID: 101, Action: "stop",
		ExpiresAt: orig, OriginalDuration: 2 * time.Hour,
	}); err != nil {
		t.Fatalf("seed ttl: %v", err)
	}
	id := &auth.Identity{UserID: "u1", ActiveTenantID: tid, EffectiveRole: "contributor"}
	r := chi.NewRouter()
	r.Post("/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/ttl/extend", func(w http.ResponseWriter, req *http.Request) {
		req = req.WithContext(auth.ContextWithIdentity(req.Context(), id))
		d.ExtendGuestTTL(w, req)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/tenants/"+tid+"/guests/pve01/qemu/101/ttl/extend", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got types.TtlExtendResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.ExpiresAt.Equal(orig.Add(2 * time.Hour)) {
		t.Fatalf("extended expiry = %v, want %v", got.ExpiresAt, orig.Add(2*time.Hour))
	}
}

func TestPutProjectTTLPolicyValidation(t *testing.T) {
	d, f, tid := ttlHarness(t)
	pid := mustPID(t, f)
	id := &auth.Identity{UserID: "u1", ActiveTenantID: tid, EffectiveRole: "owner"}
	put := func(body string) *httptest.ResponseRecorder {
		r := chi.NewRouter()
		r.Put("/api/tenants/{tenantId}/projects/{projectId}/ttl-policy", func(w http.ResponseWriter, req *http.Request) {
			req = req.WithContext(auth.ContextWithIdentity(req.Context(), id))
			d.PutProjectTTLPolicy(w, req)
		})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/tenants/"+tid+"/projects/"+pid+"/ttl-policy", strings.NewReader(body)))
		return rec
	}
	if rec := put(`{"maxTtlSeconds":86400,"defaultTtlSeconds":3600}`); rec.Code != http.StatusOK {
		t.Fatalf("valid policy status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if rec := put(`{"maxTtlSeconds":0}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("zero max status = %d, want 400", rec.Code)
	}
	if rec := put(`{"maxTtlSeconds":3600,"defaultTtlSeconds":7200}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("default > max status = %d, want 400", rec.Code)
	}
	// An absurd max that would overflow time.Duration when read back is rejected
	// (security review Low 1): 1e17 seconds is far past the 10-year ceiling.
	if rec := put(`{"maxTtlSeconds":100000000000000000}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("overflow-large max status = %d, want 400", rec.Code)
	}
}

func mustPID(t *testing.T, f *storetest.Fake) string {
	t.Helper()
	p, err := f.GetProjectByPoolID(context.Background(), "pc-acme-web")
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	return p.ID
}
