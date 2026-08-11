package handlers_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	pmx "github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
)

// guestFixture covers both guest types, a template, semicolon and comma
// joined tags, and a raw status that is not lowercase.
func guestFixture() []pmx.RawResource {
	return []pmx.RawResource{
		{ID: "node/pve01", Type: "node", Node: "pve01", Status: "online"},
		{ID: "storage/pve01/local", Type: "storage", Node: "pve01", Storage: "local", Status: "available"},

		{ID: "qemu/101", Type: "qemu", Node: "pve01", Name: "Web-Server", Status: "running",
			VMID: 101, Pool: "prod", Tags: "web;prod", CPU: 0.12, MaxCPU: 4,
			Mem: 2 * gib, MaxMem: 4 * gib, MaxDisk: 32 * gib, Uptime: 3600},
		{ID: "qemu/900", Type: "qemu", Node: "pve02", Name: "tmpl-debian", Status: "stopped",
			VMID: 900, Template: true, MaxCPU: 2, MaxMem: 2 * gib, MaxDisk: 8 * gib},
		{ID: "lxc/200", Type: "lxc", Node: "pve02", Name: "db", Status: "running",
			VMID: 200, Pool: "prod", Tags: "db,prod", CPU: 0.05, MaxCPU: 2,
			Mem: 1 * gib, MaxMem: 2 * gib, MaxDisk: 16 * gib, Uptime: 7200},
		{ID: "lxc/201", Type: "lxc", Node: "pve01", Name: "cache", Status: "STOPPED",
			VMID: 201, MaxCPU: 1, MaxMem: 1 * gib, MaxDisk: 8 * gib},
	}
}

func fixtureMock() *proxmoxtest.MockClient {
	return &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]pmx.RawResource, error) { return guestFixture(), nil },
	}
}

func vmids(guests []types.GuestSummary) []int {
	ids := []int{}
	for _, g := range guests {
		ids = append(ids, g.VMID)
	}
	return ids
}

func TestListResources(t *testing.T) {
	tests := []struct {
		name      string
		target    string
		wantVMIDs []int
	}{
		{name: "all guests, sorted by vmid", target: "/api/resources", wantVMIDs: []int{101, 200, 201, 900}},
		{name: "filter type=qemu", target: "/api/resources?type=qemu", wantVMIDs: []int{101, 900}},
		{name: "filter type=lxc", target: "/api/resources?type=lxc", wantVMIDs: []int{200, 201}},
		{name: "filter pool", target: "/api/resources?pool=prod", wantVMIDs: []int{101, 200}},
		{name: "filter node", target: "/api/resources?node=pve01", wantVMIDs: []int{101, 201}},
		{name: "filters combine", target: "/api/resources?type=lxc&pool=prod", wantVMIDs: []int{200}},
		{name: "search matches name case-insensitively", target: "/api/resources?search=WEB", wantVMIDs: []int{101}},
		{name: "search matches vmid substring", target: "/api/resources?search=20", wantVMIDs: []int{200, 201}},
		{name: "search with no hits is an empty list", target: "/api/resources?search=nomatch", wantVMIDs: []int{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doGet(t, fixtureMock(), tt.target)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}
			got := decodeBody[[]types.GuestSummary](t, rec)
			if ids := vmids(got); !reflect.DeepEqual(ids, tt.wantVMIDs) {
				t.Errorf("vmids = %v, want %v", ids, tt.wantVMIDs)
			}
		})
	}
}

func TestListResourcesMapping(t *testing.T) {
	rec := doGet(t, fixtureMock(), "/api/resources")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %q)", rec.Code, rec.Body.String())
	}
	got := decodeBody[[]types.GuestSummary](t, rec)
	byVMID := map[int]types.GuestSummary{}
	for _, g := range got {
		byVMID[g.VMID] = g
	}

	want101 := types.GuestSummary{
		ID: "qemu/101", Type: "qemu", VMID: 101, Name: "Web-Server", Node: "pve01",
		Status: "running", UptimeSec: 3600, CPUPct: 12, Cores: 4,
		MemUsed: 2 * gib, MemMax: 4 * gib, DiskMax: 32 * gib,
		Pool: "prod", Tags: []string{"web", "prod"}, Template: false,
	}
	if g := byVMID[101]; !reflect.DeepEqual(g, want101) {
		t.Errorf("vm 101 =\n%+v\nwant\n%+v", g, want101)
	}

	if g := byVMID[900]; !g.Template {
		t.Errorf("vm 900: template flag lost: %+v", g)
	}
	if g := byVMID[900]; !reflect.DeepEqual(g.Tags, []string{}) {
		t.Errorf("vm 900: empty tags must be [], got %#v", g.Tags)
	}
	if g := byVMID[200]; !reflect.DeepEqual(g.Tags, []string{"db", "prod"}) {
		t.Errorf("lxc 200: comma-joined tags = %#v, want [db prod]", g.Tags)
	}
	if g := byVMID[201]; g.Status != "stopped" {
		t.Errorf("lxc 201: status = %q, want lowercase passthrough %q", g.Status, "stopped")
	}
}

func TestListResourcesErrors(t *testing.T) {
	tests := []struct {
		name       string
		mock       *proxmoxtest.MockClient
		target     string
		wantStatus int
		wantCode   string
	}{
		{
			name:       "invalid type is rejected before calling proxmox",
			mock:       &proxmoxtest.MockClient{}, // any PVE call would panic
			target:     "/api/resources?type=docker",
			wantStatus: http.StatusBadRequest,
			wantCode:   "invalid_request",
		},
		{
			name: "proxmox unreachable surfaces as 502 envelope",
			mock: &proxmoxtest.MockClient{
				OnClusterResources: func(context.Context) ([]pmx.RawResource, error) { return nil, pveUnreachable() },
			},
			target:     "/api/resources",
			wantStatus: http.StatusBadGateway,
			wantCode:   "proxmox_unreachable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doGet(t, tt.mock, tt.target)
			wantErrorEnvelope(t, rec, tt.wantStatus, tt.wantCode)
			if tt.wantCode == "proxmox_unreachable" {
				env := decodeBody[types.ErrorEnvelope](t, rec)
				if env.Error.PVEMessage == "" {
					t.Errorf("PVEMessage must carry the verbatim transport error, got empty")
				}
			}
		})
	}
}
