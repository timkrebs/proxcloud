package handlers_test

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

// scheduleHarness wires a store-backed Deps with a router that injects a
// Contributor identity, mirroring the ResolveScope chain the real router runs
// (ownership is pre-seeded, so cross-tenant IDOR is not the subject here).
func scheduleHarness(t *testing.T) (*handlers.Deps, *storetest.Fake, string) {
	t.Helper()
	f := storetest.New()
	tid := f.AddTenant("Acme", "acme")
	pid := f.AddProject(tid, "Web", "web", "pc-acme-web")
	f.AddOwnership(tid, pid, 101, "qemu", "pve01", "active", nil)
	d := &handlers.Deps{Store: f, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return d, f, tid
}

func putSchedule(d *handlers.Deps, tenantID, body string) *httptest.ResponseRecorder {
	id := &auth.Identity{UserID: "u1", Email: "u@x.io", ActiveTenantID: tenantID, EffectiveRole: "contributor"}
	r := chi.NewRouter()
	r.Put("/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/schedule", func(w http.ResponseWriter, req *http.Request) {
		req = req.WithContext(auth.ContextWithIdentity(req.Context(), id))
		d.PutResourceSchedule(w, req)
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/tenants/"+tenantID+"/guests/pve01/qemu/101/schedule", strings.NewReader(body))
	r.ServeHTTP(rec, req)
	return rec
}

func TestPutResourceScheduleValidation(t *testing.T) {
	d, _, tid := scheduleHarness(t)
	tests := []struct {
		name string
		body string
		want int
	}{
		{"valid", `{"shutdownTime":"21:45","daysOfWeek":[1,2,3,4,5],"timezone":"Europe/Berlin","graceSeconds":120,"enabled":true}`, http.StatusOK},
		{"bad shutdown time", `{"shutdownTime":"25:99","daysOfWeek":[1],"timezone":"UTC","graceSeconds":120,"enabled":true}`, http.StatusBadRequest},
		{"non HH:MM", `{"shutdownTime":"9pm","daysOfWeek":[1],"timezone":"UTC","graceSeconds":120,"enabled":true}`, http.StatusBadRequest},
		{"empty days", `{"shutdownTime":"21:00","daysOfWeek":[],"timezone":"UTC","graceSeconds":120,"enabled":true}`, http.StatusBadRequest},
		{"day out of range", `{"shutdownTime":"21:00","daysOfWeek":[7],"timezone":"UTC","graceSeconds":120,"enabled":true}`, http.StatusBadRequest},
		{"bad timezone", `{"shutdownTime":"21:00","daysOfWeek":[1],"timezone":"Mars/Phobos","graceSeconds":120,"enabled":true}`, http.StatusBadRequest},
		{"non-positive grace", `{"shutdownTime":"21:00","daysOfWeek":[1],"timezone":"UTC","graceSeconds":0,"enabled":true}`, http.StatusBadRequest},
		{"bad auto-start", `{"shutdownTime":"21:00","autoStartTime":"nope","daysOfWeek":[1],"timezone":"UTC","graceSeconds":120,"enabled":true}`, http.StatusBadRequest},
		{"malformed json", `{`, http.StatusBadRequest},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := putSchedule(d, tid, tt.body)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestPutResourceSchedulePersists(t *testing.T) {
	d, f, tid := scheduleHarness(t)
	rec := putSchedule(d, tid, `{"shutdownTime":"21:45","daysOfWeek":[1,5],"timezone":"UTC","graceSeconds":90,"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	all := f.AllSchedules()
	if len(all) != 1 {
		t.Fatalf("stored %d schedules, want 1", len(all))
	}
	if all[0].Scope != "resource" || all[0].VMID == nil || *all[0].VMID != 101 || all[0].ShutdownTime != "21:45" {
		t.Fatalf("persisted schedule wrong: %+v", all[0])
	}
	// AutoShutdownEnabled is false in this harness, so no jobs are materialized.
	if n := len(f.AllJobs()); n != 0 {
		t.Fatalf("materialized %d jobs with the feature disabled, want 0", n)
	}
}

func TestGetResourceScheduleNotFound(t *testing.T) {
	d, _, tid := scheduleHarness(t)
	id := &auth.Identity{UserID: "u1", ActiveTenantID: tid, EffectiveRole: "reader"}
	r := chi.NewRouter()
	r.Get("/api/tenants/{tenantId}/guests/{node}/{type}/{vmid}/schedule", func(w http.ResponseWriter, req *http.Request) {
		req = req.WithContext(auth.ContextWithIdentity(req.Context(), id))
		d.GetResourceSchedule(w, req)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/tenants/"+tid+"/guests/pve01/qemu/101/schedule", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}
