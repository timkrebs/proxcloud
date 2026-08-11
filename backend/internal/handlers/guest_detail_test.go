package handlers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

func TestGetGuestDetailParsesConfig(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnGuestStatus: func(context.Context, proxmox.GuestRef) (*proxmox.GuestStatusInfo, error) {
			return &proxmox.GuestStatusInfo{Status: "running", Name: "web-01", UptimeSec: 3600, CPUPct: 12.5, Cores: 4, MemUsed: 1 << 30, MemMax: 8 << 30, Agent: true}, nil
		},
		OnGuestConfig: func(context.Context, proxmox.GuestRef) (map[string]any, error) {
			return map[string]any{
				"cores":       float64(4),
				"memory":      "8192",
				"description": "prod web",
				"onboot":      float64(1),
				"tags":        "env-prod;web",
				"net0":        "virtio=BC:24:11:AA:BB:CC,bridge=vmbr0,firewall=1,tag=20",
				"scsi0":       "local-lvm:vm-101-disk-0,iothread=1,size=32G",
				"ide2":        "local:iso/debian-12.iso,media=cdrom",
				"efidisk0":    "local-lvm:vm-101-disk-1,size=4M",
				"ostype":      "l26",
			}, nil
		},
	}
	rec, _ := do(t, mock, http.MethodGet, "/api/guests/pve01/qemu/101")
	if rec.Code != 200 {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	g := decodeBody[types.GuestDetail](t, rec)

	if g.Name != "web-01" || g.Status != "running" || !g.Agent || !g.OnBoot {
		t.Errorf("basics wrong: name=%q status=%q agent=%v onboot=%v", g.Name, g.Status, g.Agent, g.OnBoot)
	}
	if len(g.Tags) != 2 || g.Tags[0] != "env-prod" {
		t.Errorf("tags = %v", g.Tags)
	}
	if len(g.NICs) != 1 {
		t.Fatalf("nics = %+v", g.NICs)
	}
	nic := g.NICs[0]
	if nic.Model != "virtio" || nic.MAC != "BC:24:11:AA:BB:CC" || nic.Bridge != "vmbr0" || nic.VLANTag != 20 || !nic.Firewall {
		t.Errorf("nic = %+v", nic)
	}
	if len(g.Disks) != 3 {
		t.Fatalf("disks = %+v", g.Disks)
	}
	var scsi0 *types.DiskConfig
	for i := range g.Disks {
		if g.Disks[i].Key == "scsi0" {
			scsi0 = &g.Disks[i]
		}
	}
	if scsi0 == nil || scsi0.Storage != "local-lvm" || scsi0.SizeBytes != 32<<30 || scsi0.CDROM {
		t.Errorf("scsi0 = %+v", scsi0)
	}
	// DiskMax sums non-cdrom disks: 32G + 4M.
	if g.DiskMax != 32<<30+4<<20 {
		t.Errorf("diskMax = %d", g.DiskMax)
	}
}

func TestUpdateGuestConfigValidation(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantKeys   []string // keys expected in the PVE change set
		lxc        bool
	}{
		{"cores+memory qemu", `{"cores":8,"memoryMb":4096}`, 202, []string{"cores", "memory"}, false},
		{"description lxc sync", `{"description":"hi"}`, 204, []string{"description"}, true},
		{"tags joined", `{"tags":["env-prod","web"]}`, 202, []string{"tags"}, false},
		{"invalid tag", `{"tags":["Bad Tag!"]}`, 400, nil, false},
		{"cores out of range", `{"cores":0}`, 400, nil, false},
		{"empty change set", `{}`, 400, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[string]any
			mock := &proxmoxtest.MockClient{
				OnSetGuestConfig: func(_ context.Context, ref proxmox.GuestRef, changes map[string]any) (proxmox.UPID, error) {
					got = changes
					if ref.Type == "lxc" {
						return "", nil
					}
					return "UPID:pve01:1:2:3:qmconfig:101:root@pam:", nil
				},
			}
			typ := "qemu"
			if tt.lxc {
				typ = "lxc"
			}
			rec, _ := doBody(t, mock, http.MethodPatch, "/api/guests/pve01/"+typ+"/101/config", tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body)
			}
			for _, k := range tt.wantKeys {
				if _, ok := got[k]; !ok {
					t.Errorf("change set missing %q: %v", k, got)
				}
			}
		})
	}
}

func TestGuestInterfacesAgentUnavailable(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnAgentInterfaces: func(context.Context, proxmox.GuestRef) ([]types.GuestNIC, error) {
			return nil, proxmox.ErrAgentUnavailable
		},
	}
	rec, _ := do(t, mock, http.MethodGet, "/api/guests/pve01/qemu/101/interfaces")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	l := decodeBody[types.GuestNICList](t, rec)
	if !l.AgentUnavailable || len(l.NICs) != 0 {
		t.Errorf("list = %+v, want honest agentUnavailable state", l)
	}
}

func TestResizeValidation(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnResizeDisk: func(_ context.Context, _ proxmox.GuestRef, disk, size string) (proxmox.UPID, error) {
			if disk != "scsi0" || size != "64G" {
				t.Errorf("resize args = %q %q", disk, size)
			}
			return "UPID:pve01:1:2:3:resize:101:root@pam:", nil
		},
	}
	rec, _ := doBody(t, mock, http.MethodPost, "/api/guests/pve01/qemu/101/resize", `{"disk":"scsi0","sizeGib":64}`)
	if rec.Code != 202 {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	rec, _ = doBody(t, mock, http.MethodPost, "/api/guests/pve01/qemu/101/resize", `{"disk":"../etc","sizeGib":64}`)
	wantErrorEnvelope(t, rec, 400, "invalid_request")
}

// doBody is do() with a JSON request body.
func doBody(t *testing.T, mock *proxmoxtest.MockClient, method, target, body string) (*httptest.ResponseRecorder, struct{}) {
	t.Helper()
	reg := tasks.NewRegistry()
	d := &handlers.Deps{
		PVE:      mock,
		Log:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registry: reg,
		Broker:   events.NewBroker(),
	}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) { d.Mount(r) })
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(method, target, strings.NewReader(body)))
	return rec, struct{}{}
}
