package handlers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

// do performs one request against a fully-wired Deps (registry + broker),
// returning the recorder and the registry for overlay assertions.
func do(t *testing.T, mock *proxmoxtest.MockClient, method, target string) (*httptest.ResponseRecorder, *tasks.Registry) {
	t.Helper()
	reg := tasks.NewRegistry()
	d := &handlers.Deps{
		PVE:      mock,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registry: reg,
		Broker:   events.NewBroker(),
	}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) { mountLegacy(d, r) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec, reg
}

func stubResources() func(ctx context.Context) ([]proxmox.RawResource, error) {
	return func(context.Context) ([]proxmox.RawResource, error) {
		return []proxmox.RawResource{
			{ID: "qemu/101", Type: "qemu", VMID: 101, Name: "web-01", Node: "pve01", Status: "running"},
			{ID: "lxc/200", Type: "lxc", VMID: 200, Name: "cache-01", Node: "pve01", Status: "stopped"},
		}, nil
	}
}

func TestGuestAction(t *testing.T) {
	tests := []struct {
		name       string
		target     string
		wantStatus int
		wantAction string // expected PVE action passed through
		wantLabel  string
		wantCode   string // error code when failing
	}{
		{"start qemu", "/api/guests/pve01/qemu/101/start", 202, "start", "Start virtual machine", ""},
		{"stop lxc", "/api/guests/pve01/lxc/200/stop", 202, "stop", "Stop container", ""},
		{"shutdown qemu", "/api/guests/pve01/qemu/101/shutdown", 202, "shutdown", "Shut down virtual machine", ""},
		{"reboot qemu", "/api/guests/pve01/qemu/101/reboot", 202, "reboot", "Restart virtual machine", ""},
		{"reset qemu", "/api/guests/pve01/qemu/101/reset", 202, "reset", "Reset virtual machine", ""},
		{"reset lxc rejected", "/api/guests/pve01/lxc/200/reset", 400, "", "", "invalid_request"},
		{"unknown action", "/api/guests/pve01/qemu/101/hibernate", 404, "", "", "not_found"},
		{"bad type", "/api/guests/pve01/kvm/101/start", 404, "", "", "not_found"},
		{"bad vmid", "/api/guests/pve01/qemu/abc/start", 404, "", "", "not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAction string
			mock := &proxmoxtest.MockClient{
				OnClusterResources: stubResources(),
				OnGuestAction: func(_ context.Context, ref proxmox.GuestRef, action string) (proxmox.UPID, error) {
					gotAction = action
					return "UPID:pve01:0001:0002:0003:qmstart:101:root@pam:", nil
				},
			}
			rec, reg := do(t, mock, http.MethodPost, tt.target)

			if tt.wantCode != "" {
				wantErrorEnvelope(t, rec, tt.wantStatus, tt.wantCode)
				return
			}
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if gotAction != tt.wantAction {
				t.Errorf("PVE action = %q, want %q", gotAction, tt.wantAction)
			}
			ref := decodeBody[types.TaskRef](t, rec)
			if ref.UPID == "" || ref.Action != tt.wantLabel {
				t.Errorf("TaskRef = %+v, want action %q", ref, tt.wantLabel)
			}
			// The registry must now report a transitional status for the VMID.
			vmid := 101
			if tt.target[len(tt.target)-9:] == "/200/stop" {
				vmid = 200
			}
			if _, _, ok := reg.ActiveFor(vmid); !ok {
				t.Errorf("registry has no active task for vmid %d", vmid)
			}
		})
	}
}

// TestGuestStartClearsLifecycleMarkers proves a user-initiated start clears BOTH
// the scheduler-stop marker (auto_stopped, ADR-0019) and the TTL-expiry marker
// (expired_at, ADR-0020) — so a guest a user turns back on is no longer labelled
// "stopped by schedule" or "expired" (honest task states).
func TestGuestStartClearsLifecycleMarkers(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid := f.AddTenant("Acme", "acme")
	pid := f.AddProject(tid, "P1", "p1", "pc-acme-p1")
	f.AddOwnership(tid, pid, 101, "qemu", "pve01", "active", nil)
	if err := f.SetAutoStopped(ctx, 101, true); err != nil {
		t.Fatalf("seed auto_stopped: %v", err)
	}
	now := time.Now()
	if err := f.SetExpiredAt(ctx, 101, &now); err != nil {
		t.Fatalf("seed expired_at: %v", err)
	}

	mock := &proxmoxtest.MockClient{
		OnClusterResources: stubResources(),
		OnGuestAction: func(_ context.Context, _ proxmox.GuestRef, _ string) (proxmox.UPID, error) {
			return "UPID:pve01:0001:0002:0003:qmstart:101:root@pam:", nil
		},
	}
	d := &handlers.Deps{
		PVE: mock, Store: f, Registry: tasks.NewRegistry(), Broker: events.NewBroker(),
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) { mountLegacy(d, r) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/guests/pve01/qemu/101/start", nil))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %q)", rec.Code, rec.Body.String())
	}

	own, err := f.GetOwnershipByVMID(ctx, 101)
	if err != nil {
		t.Fatalf("GetOwnershipByVMID: %v", err)
	}
	if own.AutoStopped {
		t.Error("auto_stopped not cleared on manual start")
	}
	if own.ExpiredAt != nil {
		t.Error("expired_at not cleared on manual start")
	}
}

func TestDeleteGuest(t *testing.T) {
	tests := []struct {
		name        string
		guestStatus string
		body        string
		wantStatus  int
		wantCode    string
		wantPurge   bool
		target      string
	}{
		{"running guest rejected", "running", `{"confirmName":"victim"}`, 409, "conflict", false, "/api/guests/pve01/qemu/101"},
		{"stopped guest deleted with purge", "stopped", `{"confirmName":"victim"}`, 202, "", true, "/api/guests/pve01/qemu/101?purge=1"},
		{"stopped guest deleted without purge", "stopped", `{"confirmName":"victim"}`, 202, "", false, "/api/guests/pve01/lxc/200"},
		{"wrong confirm name rejected", "stopped", `{"confirmName":"other"}`, 400, "invalid_request", false, "/api/guests/pve01/qemu/101?purge=1"},
		{"missing body rejected", "stopped", ``, 400, "invalid_request", false, "/api/guests/pve01/qemu/101?purge=1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPurge bool
			deleted := false
			mock := &proxmoxtest.MockClient{
				OnGuestStatus: func(context.Context, proxmox.GuestRef) (*proxmox.GuestStatusInfo, error) {
					return &proxmox.GuestStatusInfo{Status: tt.guestStatus, Name: "victim"}, nil
				},
				OnDeleteGuest: func(_ context.Context, _ proxmox.GuestRef, purge bool) (proxmox.UPID, error) {
					deleted, gotPurge = true, purge
					return "UPID:pve01:0001:0002:0003:qmdestroy:101:root@pam:", nil
				},
			}
			rec, _ := doBody(t, mock, http.MethodDelete, tt.target, tt.body)

			if tt.wantCode != "" {
				wantErrorEnvelope(t, rec, tt.wantStatus, tt.wantCode)
				if deleted {
					t.Fatal("DeleteGuest was called despite rejection")
				}
				return
			}
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !deleted || gotPurge != tt.wantPurge {
				t.Errorf("deleted=%v purge=%v, want deleted=true purge=%v", deleted, gotPurge, tt.wantPurge)
			}
		})
	}
}

func TestResourcesTransitionalOverlay(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnClusterResources: stubResources(),
		OnGuestAction: func(context.Context, proxmox.GuestRef, string) (proxmox.UPID, error) {
			return "UPID:pve01:0001:0002:0003:qmstop:101:root@pam:", nil
		},
	}
	reg := tasks.NewRegistry()
	d := &handlers.Deps{PVE: mock, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Registry: reg, Broker: events.NewBroker()}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) { mountLegacy(d, r) })

	// Submit a stop, then list resources: 101 must show the overlay.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/guests/pve01/qemu/101/stop", nil))
	if rec.Code != 202 {
		t.Fatalf("action status = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/resources", nil))
	list := decodeBody[[]types.GuestSummary](t, rec)

	var g101, g200 *types.GuestSummary
	for i := range list {
		switch list[i].VMID {
		case 101:
			g101 = &list[i]
		case 200:
			g200 = &list[i]
		}
	}
	if g101 == nil || g101.Status != "stopping" || g101.PendingTaskUPID == "" {
		t.Errorf("vmid 101 = %+v, want status stopping with pending UPID", g101)
	}
	if g200 == nil || g200.Status != "stopped" || g200.PendingTaskUPID != "" {
		t.Errorf("vmid 200 = %+v, want untouched stopped status", g200)
	}

	// Completing the task removes the overlay.
	reg.Complete("UPID:pve01:0001:0002:0003:qmstop:101:root@pam:", true, "OK")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/resources", nil))
	for _, g := range decodeBody[[]types.GuestSummary](t, rec) {
		if g.VMID == 101 && g.Status != "running" {
			t.Errorf("after completion vmid 101 status = %q, want running (raw PVE value)", g.Status)
		}
	}
}
