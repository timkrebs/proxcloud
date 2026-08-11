package handlers_test

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	pmx "github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
)

const gib = int64(1 << 30)

// clusterFixture is a two-node cluster with guests, a template, per-node
// local storages, and one shared storage reported by both nodes.
func clusterFixture() []pmx.RawResource {
	return []pmx.RawResource{
		{ID: "node/pve01", Type: "node", Node: "pve01", Status: "online",
			CPU: 0.5, MaxCPU: 8, Mem: 16 * gib, MaxMem: 32 * gib},
		{ID: "node/pve02", Type: "node", Node: "pve02", Status: "online",
			CPU: 0.25, MaxCPU: 4, Mem: 8 * gib, MaxMem: 16 * gib},

		{ID: "qemu/101", Type: "qemu", Node: "pve01", Name: "web", Status: "running", VMID: 101},
		{ID: "qemu/102", Type: "qemu", Node: "pve02", Name: "batch", Status: "stopped", VMID: 102},
		{ID: "qemu/900", Type: "qemu", Node: "pve01", Name: "tmpl-debian", Status: "stopped", VMID: 900, Template: true},
		{ID: "lxc/200", Type: "lxc", Node: "pve02", Name: "db", Status: "running", VMID: 200},

		{ID: "storage/pve01/local", Type: "storage", Node: "pve01", Storage: "local",
			Status: "available", Disk: 100, MaxDisk: 200},
		{ID: "storage/pve02/local", Type: "storage", Node: "pve02", Storage: "local",
			Status: "available", Disk: 50, MaxDisk: 200},
		{ID: "storage/pve01/nfs", Type: "storage", Node: "pve01", Storage: "nfs",
			Status: "available", Shared: true, Disk: 500, MaxDisk: 1000},
		{ID: "storage/pve02/nfs", Type: "storage", Node: "pve02", Storage: "nfs",
			Status: "available", Shared: true, Disk: 500, MaxDisk: 1000},
	}
}

func TestGetCluster(t *testing.T) {
	okMock := func(rows []pmx.RawResource) *proxmoxtest.MockClient {
		return &proxmoxtest.MockClient{
			OnVersion: func(context.Context) (string, error) { return "8.2.4", nil },
			OnClusterStatus: func(context.Context) (*pmx.ClusterInfo, error) {
				return &pmx.ClusterInfo{Name: "homelab", Quorate: true, NodesOnline: 2, NodesTotal: 2}, nil
			},
			OnClusterResources: func(context.Context) ([]pmx.RawResource, error) { return rows, nil },
		}
	}

	tests := []struct {
		name       string
		mock       *proxmoxtest.MockClient
		wantStatus int
		check      func(t *testing.T, rec *httptest.ResponseRecorder)
	}{
		{
			name:       "aggregates guests, weighted cpu, and deduped shared storage",
			mock:       okMock(clusterFixture()),
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				got := decodeBody[types.ClusterSummary](t, rec)
				if got.Name != "homelab" || !got.Quorate || got.PVEVersion != "8.2.4" ||
					got.NodesOnline != 2 || got.NodesTotal != 2 {
					t.Errorf("identity block wrong: %+v", got)
				}
				wantGuests := types.GuestCounts{VMsRunning: 1, VMsTotal: 2, LXCsRunning: 1, LXCsTotal: 1}
				if got.Guests != wantGuests {
					t.Errorf("guests = %+v, want %+v (template must not count)", got.Guests, wantGuests)
				}
				// CPU weighted by maxcpu: (0.5*8 + 0.25*4) / 12 * 100.
				if wantCPU := 5.0 / 12.0 * 100; math.Abs(got.Usage.CPUPct-wantCPU) > 1e-9 {
					t.Errorf("cpuPct = %v, want %v", got.Usage.CPUPct, wantCPU)
				}
				if got.Usage.MemUsed != 24*gib || got.Usage.MemTotal != 48*gib {
					t.Errorf("mem = %d/%d, want %d/%d", got.Usage.MemUsed, got.Usage.MemTotal, 24*gib, 48*gib)
				}
				// Shared "nfs" counted once; per-node "local" twice.
				if got.Usage.DiskUsed != 650 || got.Usage.DiskTotal != 1400 {
					t.Errorf("disk = %d/%d, want 650/1400", got.Usage.DiskUsed, got.Usage.DiskTotal)
				}
			},
		},
		{
			name: "offline node contributes no usage",
			mock: okMock([]pmx.RawResource{
				{ID: "node/pve01", Type: "node", Node: "pve01", Status: "online",
					CPU: 0.5, MaxCPU: 8, Mem: 16 * gib, MaxMem: 32 * gib},
				{ID: "node/pve02", Type: "node", Node: "pve02", Status: "offline",
					CPU: 0.9, MaxCPU: 64, Mem: 99 * gib, MaxMem: 128 * gib},
			}),
			wantStatus: http.StatusOK,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				got := decodeBody[types.ClusterSummary](t, rec)
				if math.Abs(got.Usage.CPUPct-50) > 1e-9 {
					t.Errorf("cpuPct = %v, want 50 (offline node must be excluded)", got.Usage.CPUPct)
				}
				if got.Usage.MemTotal != 32*gib {
					t.Errorf("memTotal = %d, want %d", got.Usage.MemTotal, 32*gib)
				}
			},
		},
		{
			name: "proxmox unreachable surfaces as 502 envelope",
			mock: &proxmoxtest.MockClient{
				OnClusterStatus: func(context.Context) (*pmx.ClusterInfo, error) { return nil, pveUnreachable() },
			},
			wantStatus: http.StatusBadGateway,
			check: func(t *testing.T, rec *httptest.ResponseRecorder) {
				wantErrorEnvelope(t, rec, http.StatusBadGateway, "proxmox_unreachable")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doGet(t, tt.mock, "/api/cluster")
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			tt.check(t, rec)
		})
	}
}

func TestGetNextID(t *testing.T) {
	tests := []struct {
		name       string
		mock       *proxmoxtest.MockClient
		wantStatus int
		wantVMID   int
		wantCode   string
	}{
		{
			name: "returns next free vmid",
			mock: &proxmoxtest.MockClient{
				OnNextID: func(context.Context) (int, error) { return 105, nil },
			},
			wantStatus: http.StatusOK,
			wantVMID:   105,
		},
		{
			name: "proxmox unreachable surfaces as 502 envelope",
			mock: &proxmoxtest.MockClient{
				OnNextID: func(context.Context) (int, error) { return 0, pveUnreachable() },
			},
			wantStatus: http.StatusBadGateway,
			wantCode:   "proxmox_unreachable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doGet(t, tt.mock, "/api/cluster/nextid")
			if tt.wantCode != "" {
				wantErrorEnvelope(t, rec, tt.wantStatus, tt.wantCode)
				return
			}
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if got := decodeBody[types.NextID](t, rec); got.VMID != tt.wantVMID {
				t.Errorf("vmid = %d, want %d", got.VMID, tt.wantVMID)
			}
		})
	}
}
