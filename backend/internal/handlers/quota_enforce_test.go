package handlers_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
)

func iptr(v int) *int { return &v }

// pveCreateCounter wires every Proxmox create/clone/pool method to a shared
// atomic counter, so a test can prove the create path never reached Proxmox.
func pveCreateCounter(mock *proxmoxtest.MockClient, snap func(context.Context) ([]proxmox.RawResource, error)) *int32 {
	var n int32
	mock.OnClusterResources = snap
	mock.OnCreatePool = func(context.Context, string, string) error { atomic.AddInt32(&n, 1); return nil }
	mock.OnCreateVM = func(context.Context, string, map[string]any) (proxmox.UPID, error) {
		atomic.AddInt32(&n, 1)
		return "UPID:pve01:0:0:0:qmcreate:1:u@pam:", nil
	}
	mock.OnCreateLXC = func(context.Context, string, map[string]any) (proxmox.UPID, error) {
		atomic.AddInt32(&n, 1)
		return "UPID:pve01:0:0:0:vzcreate:1:u@pam:", nil
	}
	mock.OnCloneGuest = func(context.Context, proxmox.GuestRef, int, string, string, bool, string) (proxmox.UPID, error) {
		atomic.AddInt32(&n, 1)
		return "UPID:pve01:0:0:0:qmclone:1:u@pam:", nil
	}
	return &n
}

// --- over-quota create is refused BEFORE any Proxmox create call ---

func TestCreateOverQuotaRefusedBeforePVE(t *testing.T) {
	mock := &proxmoxtest.MockClient{}
	// Snapshot: active guest 101 provisions 4 vCPU — the tenant's whole cap.
	calls := pveCreateCounter(mock, func(context.Context) ([]proxmox.RawResource, error) {
		return []proxmox.RawResource{
			{ID: "qemu/101", Type: "qemu", VMID: 101, Node: "pve01", MaxCPU: 4},
		}, nil
	})
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	hh.fake.AddQuota("tenant", tenantA, iptr(4), nil, nil, nil) // MaxVCPU=4
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userA)

	// Requesting 2 more vCPU pushes 4+2 > 4 → refused.
	body := `{"type":"lxc","name":"cache-02","node":"pve01","vmid":200,"projectId":"` + projA + `",
		"source":{"mode":"vztmpl","vztmplVolId":"local:vztmpl/x.tar.gz"},
		"cores":2,"memoryMb":512,"diskGb":8,"storage":"local","bridge":"vmbr0"}`
	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/guests", body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("over-quota create = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	env := decodeBody[types.ErrorEnvelope](t, rec)
	if env.Error.Code != "quota_exceeded" {
		t.Fatalf("error code = %q, want quota_exceeded (msg %q)", env.Error.Code, env.Error.Message)
	}
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Fatalf("Proxmox create path was called %d times, want 0 (enforced pre-PVE)", n)
	}
	if s := hh.fake.OwnershipStatus(200); s != "" {
		t.Fatalf("a reservation leaked for a refused create: status %q", s)
	}
}

// --- a pending (in-flight) reservation counts toward usage ---

func TestPendingReservationCountsTowardQuota(t *testing.T) {
	createUPID := proxmox.UPID("UPID:pve01:1:2:3:vzcreate:200:u@pam:")
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return nil, nil },
		OnCreatePool:       func(context.Context, string, string) error { return nil },
		OnCreateLXC:        func(context.Context, string, map[string]any) (proxmox.UPID, error) { return createUPID, nil },
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	hh.fake.AddQuota("tenant", tenantA, iptr(4), nil, nil, nil) // MaxVCPU=4
	c := hh.cookie(t, userA)

	// Create #1 reserves the full cap (4 vCPU) as a pending row.
	body1 := `{"type":"lxc","name":"cache-a","node":"pve01","vmid":200,"projectId":"` + projA + `",
		"source":{"mode":"vztmpl","vztmplVolId":"local:vztmpl/x.tar.gz"},
		"cores":4,"memoryMb":512,"diskGb":8,"storage":"local","bridge":"vmbr0"}`
	if rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/guests", body1); rec.Code != http.StatusAccepted {
		t.Fatalf("create #1 = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	if s := hh.fake.OwnershipStatus(200); s != "pending" {
		t.Fatalf("reservation #1 status = %q, want pending", s)
	}

	// Create #2 asks for 1 more vCPU: 4 (pending) + 1 > 4 → refused because the
	// in-flight reservation is already counted.
	body2 := `{"type":"lxc","name":"cache-b","node":"pve01","vmid":201,"projectId":"` + projA + `",
		"source":{"mode":"vztmpl","vztmplVolId":"local:vztmpl/x.tar.gz"},
		"cores":1,"memoryMb":512,"diskGb":8,"storage":"local","bridge":"vmbr0"}`
	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/guests", body2)
	if rec.Code != http.StatusConflict {
		t.Fatalf("create #2 = %d, want 409 (pending reservation must count) (body %s)", rec.Code, rec.Body.String())
	}
	if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "quota_exceeded" {
		t.Fatalf("create #2 error code = %q, want quota_exceeded", env.Error.Code)
	}
	if s := hh.fake.OwnershipStatus(201); s != "" {
		t.Fatalf("a reservation leaked for the refused create #2: status %q", s)
	}
}

// --- clone delta comes from the SOURCE template, not the request sizing ---

func TestCloneQuotaDeltaFromSource(t *testing.T) {
	mock := &proxmoxtest.MockClient{}
	// Template 900 provisions 8 vCPU; the tenant cap is 10 (so the template alone
	// fits, but template + an 8-vCPU clone would not).
	calls := pveCreateCounter(mock, func(context.Context) ([]proxmox.RawResource, error) {
		return []proxmox.RawResource{
			{ID: "qemu/900", Type: "qemu", VMID: 900, Node: "pve01", MaxCPU: 8, Template: true},
		}, nil
	})
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Templates", "tmpl", "pc-a-tmpl")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	hh.fake.AddQuota("tenant", tenantA, iptr(10), nil, nil, nil) // MaxVCPU=10
	hh.fake.AddOwnership(tenantA, projA, 900, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userA)

	// The clone REQUESTS only 1 vCPU. If the delta were taken from the request it
	// would fit (8 used + 1 = 9 ≤ 10); taking it from the source (8) makes
	// 8 + 8 = 16 > 10 → refused. A 409 proves the delta came from the source.
	body := `{"type":"qemu","name":"clone-1","node":"pve01","vmid":300,"projectId":"` + projA + `",
		"source":{"mode":"clone","cloneVmid":900,"cloneMode":"full"},
		"cores":1,"memoryMb":512}`
	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/guests", body)

	if rec.Code != http.StatusConflict {
		t.Fatalf("clone over-quota = %d, want 409 (delta must come from source) (body %s)", rec.Code, rec.Body.String())
	}
	if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "quota_exceeded" {
		t.Fatalf("clone error code = %q, want quota_exceeded", env.Error.Code)
	}
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Fatalf("Proxmox clone path was called %d times, want 0", n)
	}
	if s := hh.fake.OwnershipStatus(300); s != "" {
		t.Fatalf("a reservation leaked for the refused clone: status %q", s)
	}
}
