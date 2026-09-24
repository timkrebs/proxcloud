package handlers_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

// --- M-a.2: snapshot rollback is gated through the growth reservation ---

// TestRollbackOverQuotaBlocked: rolling back to a snapshot whose STORED config
// (cores/memory) exceeds the tenant cap must 409 BEFORE any rollback call; a
// snapshot at or below the current size rolls back normally.
func TestRollbackOverQuotaBlocked(t *testing.T) {
	var rollbacks int32
	snapConfigs := map[string]map[string]any{
		// "big" was taken when the guest ran 8 cores / 8 GiB (memory as the
		// string PVE 8/9 emits); "same" matches the current 2 / 2048.
		"big":  {"cores": float64(8), "memory": "8192"},
		"same": {"cores": float64(2), "memory": float64(2048)},
	}
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) {
			return []proxmox.RawResource{
				{ID: "qemu/101", Type: "qemu", VMID: 101, Node: "pve01", MaxCPU: 2, MaxMem: 2048 << 20, MaxDisk: 10 << 30},
			}, nil
		},
		OnSnapshotConfig: func(_ context.Context, _ proxmox.GuestRef, name string) (map[string]any, error) {
			return snapConfigs[name], nil
		},
		OnRollbackSnapshot: func(context.Context, proxmox.GuestRef, string) (proxmox.UPID, error) {
			atomic.AddInt32(&rollbacks, 1)
			return "UPID:pve01:0:0:0:qmrollback:101:u@pam:", nil
		},
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	hh.fake.AddQuota("tenant", tenantA, iptr(2), qti64ptr(2048), nil, nil) // caps == current size
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userA)
	base := "/api/tenants/" + tenantA + "/guests/pve01/qemu/101/snapshots"

	rec := hh.req(t, c, http.MethodPost, base+"/big/rollback", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("over-quota rollback = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "quota_exceeded" {
		t.Fatalf("rollback error code = %q, want quota_exceeded", env.Error.Code)
	}
	if n := atomic.LoadInt32(&rollbacks); n != 0 {
		t.Fatalf("RollbackSnapshot reached Proxmox %d times on a refused rollback, want 0", n)
	}

	// A snapshot at the current size passes and reaches Proxmox.
	if rec := hh.req(t, c, http.MethodPost, base+"/same/rollback", ""); rec.Code != http.StatusAccepted {
		t.Fatalf("in-quota rollback = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	if n := atomic.LoadInt32(&rollbacks); n != 1 {
		t.Fatalf("RollbackSnapshot calls = %d, want 1", n)
	}
}

// --- M-a.3: the resize delta is measured against the NAMED disk's own size ---

// TestResizePerDiskDelta: growing a non-boot disk must be charged by ITS OWN
// current size from the guest config, not the boot-disk maxdisk. The old code
// compared the target to the boot disk (32G here), so growing the 10G data
// disk to 20G looked like a shrink and bypassed quota entirely.
func TestResizePerDiskDelta(t *testing.T) {
	newMock := func(resizes *int32) *proxmoxtest.MockClient {
		return &proxmoxtest.MockClient{
			// Boot disk 32 GiB is what the cluster snapshot reports as maxdisk.
			OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) {
				return []proxmox.RawResource{
					{ID: "qemu/101", Type: "qemu", VMID: 101, Node: "pve01", MaxCPU: 2, MaxMem: 2048 << 20, MaxDisk: 32 << 30},
				}, nil
			},
			OnGuestConfig: func(context.Context, proxmox.GuestRef) (map[string]any, error) {
				return map[string]any{
					"scsi0": "local-lvm:vm-101-disk-0,size=32G", // boot
					"scsi1": "local-lvm:vm-101-disk-1,size=10G", // data
					"ide2":  "local:iso/x.iso,media=cdrom",
				}, nil
			},
			OnResizeDisk: func(context.Context, proxmox.GuestRef, string, string) (proxmox.UPID, error) {
				atomic.AddInt32(resizes, 1)
				return "UPID:pve01:0:0:0:resize:101:u@pam:", nil
			},
		}
	}
	seed := func(mock *proxmoxtest.MockClient, diskCapGiB int64) (*harness, *http.Cookie, string) {
		hh := newHarness(t, mock)
		tenantA := hh.fake.AddTenant("A", "a")
		projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
		userA := hh.fake.AddUser("a@x.io", "Ada", false)
		hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
		hh.fake.AddQuota("tenant", tenantA, nil, nil, qti64ptr(diskCapGiB), nil)
		hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
		return hh, hh.cookie(t, userA), "/api/tenants/" + tenantA + "/guests/pve01/qemu/101/resize"
	}

	t.Run("non-boot grow below boot size is still a charged grow", func(t *testing.T) {
		var resizes int32
		hh, c, path := seed(newMock(&resizes), 32) // cap == current boot usage: NO headroom
		// scsi1 10G -> 20G: target < boot(32) — the old boot-disk compare saw a
		// "shrink" and skipped quota; the honest per-disk delta is +10 → 409.
		rec := hh.req(t, c, http.MethodPost, path, `{"disk":"scsi1","sizeGib":20}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("non-boot grow = %d, want 409 (per-disk delta must be charged) (body %s)", rec.Code, rec.Body.String())
		}
		if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "quota_exceeded" {
			t.Fatalf("error code = %q, want quota_exceeded", env.Error.Code)
		}
		if atomic.LoadInt32(&resizes) != 0 {
			t.Fatal("ResizeDisk reached Proxmox on a refused grow")
		}
	})

	t.Run("grow within headroom passes and reserves the footprint", func(t *testing.T) {
		var resizes int32
		hh, c, path := seed(newMock(&resizes), 45) // headroom 13 GiB
		rec := hh.req(t, c, http.MethodPost, path, `{"disk":"scsi1","sizeGib":20}`)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("in-quota grow = %d, want 202 (body %s)", rec.Code, rec.Body.String())
		}
		own, err := hh.fake.GetOwnershipByVMID(context.Background(), 101)
		if err != nil {
			t.Fatalf("GetOwnershipByVMID: %v", err)
		}
		if own.ReservedDiskGB == nil || *own.ReservedDiskGB != 42 {
			t.Fatalf("reserved_disk_gb = %v, want 42 (live 32 + per-disk delta 10)", own.ReservedDiskGB)
		}
		if atomic.LoadInt32(&resizes) != 1 {
			t.Fatal("ResizeDisk not called exactly once for the allowed grow")
		}
	})

	t.Run("shrink and unknown-disk handling", func(t *testing.T) {
		var resizes int32
		hh, c, path := seed(newMock(&resizes), 32)
		// Shrink of scsi1 (10G -> 8G): no growth, no quota involvement.
		if rec := hh.req(t, c, http.MethodPost, path, `{"disk":"scsi1","sizeGib":8}`); rec.Code != http.StatusAccepted {
			t.Fatalf("shrink = %d, want 202 (body %s)", rec.Code, rec.Body.String())
		}
		// A disk key absent from the config → explicit 400, never a guessed size.
		if rec := hh.req(t, c, http.MethodPost, path, `{"disk":"scsi5","sizeGib":50}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("unknown disk = %d, want 400 (body %s)", rec.Code, rec.Body.String())
		}
		// A CD-ROM drive cannot be resized.
		if rec := hh.req(t, c, http.MethodPost, path, `{"disk":"ide2","sizeGib":50}`); rec.Code != http.StatusBadRequest {
			t.Fatalf("cdrom resize = %d, want 400 (body %s)", rec.Code, rec.Body.String())
		}
	})
}

// --- M-a.1: the growth reservation is visible to a subsequent create ---

// TestGrowthReservationVisibleToCreate: after a config-grow is accepted, the
// reserved footprint must count against a following create's quota check even
// though the live snapshot still reports the old size (the old check-then-act
// let both land in the same headroom).
func TestGrowthReservationVisibleToCreate(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		// No sockets key: PVE's default of 1 socket, so vCPU == cores.
		OnGuestConfig: func(context.Context, proxmox.GuestRef) (map[string]any, error) {
			return map[string]any{"cores": float64(2)}, nil
		},
		OnSetGuestConfig: func(context.Context, proxmox.GuestRef, map[string]any) (proxmox.UPID, error) {
			return "UPID:pve01:0:0:0:qmconfig:101:u@pam:", nil
		},
	}
	// The snapshot keeps reporting guest 101 at 2 vCPU — PVE has not applied the
	// grow yet. The reservation alone must close the gap.
	calls := pveCreateCounter(mock, func(context.Context) ([]proxmox.RawResource, error) {
		return []proxmox.RawResource{
			{ID: "qemu/101", Type: "qemu", VMID: 101, Node: "pve01", MaxCPU: 2, MaxMem: 2048 << 20, MaxDisk: 10 << 30},
		}, nil
	})
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	hh.fake.AddQuota("tenant", tenantA, iptr(8), nil, nil, nil) // MaxVCPU=8
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userA)

	// Grow guest 101 from 2 to 6 vCPU: 2+4 ≤ 8 → accepted, footprint reserved.
	rec := hh.req(t, c, http.MethodPatch, "/api/tenants/"+tenantA+"/guests/pve01/qemu/101/config", `{"cores":6}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("grow = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	own, err := hh.fake.GetOwnershipByVMID(context.Background(), 101)
	if err != nil || own.ReservedVCPU == nil || *own.ReservedVCPU != 6 {
		t.Fatalf("reserved_vcpu = %v (err %v), want 6", own.ReservedVCPU, err)
	}

	// Create asking for 4 vCPU: usage now counts max(live 2, reserved 6) = 6, so
	// 6+4 > 8 → 409. Without the reservation this was 2+4 ≤ 8 → over-commit.
	body := `{"type":"lxc","name":"cache-02","node":"pve01","vmid":200,"projectId":"` + projA + `",
		"source":{"mode":"vztmpl","vztmplVolId":"local:vztmpl/x.tar.gz"},
		"cores":4,"memoryMb":512,"diskGb":8,"storage":"local","bridge":"vmbr0"}`
	rec = hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/guests", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("create after grow = %d, want 409 (reservation must count) (body %s)", rec.Code, rec.Body.String())
	}
	if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "quota_exceeded" {
		t.Fatalf("create error code = %q, want quota_exceeded", env.Error.Code)
	}
	if n := atomic.LoadInt32(calls); n != 0 {
		t.Fatalf("Proxmox create path was called %d times, want 0", n)
	}
}

// --- M-a.4: two concurrent grows into the same headroom — one must lose ---

// TestConcurrentGrowsOneWins drives the fake store's ReserveGuestGrowth from
// two goroutines racing the last 4 vCPU of headroom; the serialized
// check+persist must admit exactly one (the Postgres advisory-lock variant is
// proven by the integration test TestReserveGuestGrowthRaceRespectsCap).
func TestConcurrentGrowsOneWins(t *testing.T) {
	fake := storetest.New()
	tenantA := fake.AddTenant("A", "a")
	projA := fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	fake.AddQuota("tenant", tenantA, iptr(8), nil, nil, nil)
	fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	fake.AddOwnership(tenantA, projA, 102, "qemu", "pve01", "active", nil)
	snap := map[int]store.Alloc{101: {VCPU: 2}, 102: {VCPU: 2}} // usage 4, headroom 4

	var successes, quotaHits int64
	var wg sync.WaitGroup
	for _, vmid := range []int{101, 102} {
		wg.Add(1)
		go func(vmid int) {
			defer wg.Done()
			err := fake.ReserveGuestGrowth(context.Background(), store.ReserveGrowthParams{
				TenantID: tenantA, ProjectID: projA, VMID: vmid, Snapshot: snap,
				TargetVCPU: 6, // +4 each; only one fits
			})
			var qe store.ErrQuotaExceeded
			switch {
			case err == nil:
				atomic.AddInt64(&successes, 1)
			case errors.As(err, &qe):
				atomic.AddInt64(&quotaHits, 1)
			default:
				t.Errorf("vmid %d: unexpected error: %v", vmid, err)
			}
		}(vmid)
	}
	wg.Wait()

	if successes != 1 || quotaHits != 1 {
		t.Fatalf("successes=%d quotaHits=%d, want exactly 1/1 (no double-spend of headroom)", successes, quotaHits)
	}
	tenantUsage, _, err := fake.ComputeUsage(context.Background(), tenantA, snap)
	if err != nil {
		t.Fatalf("ComputeUsage: %v", err)
	}
	if tenantUsage.VCPU > 8 {
		t.Fatalf("usage after race = %d vCPU, exceeds the cap 8", tenantUsage.VCPU)
	}
}

// --- Repeated data-disk grows are each charged; reservations never go down ---

// TestResizeRepeatedDataDiskGrowIsCharged replays the security review's
// proof of concept: tenant disk cap 45 GiB, boot disk 32 GiB, data disk scsi1
// 10 GiB. Every scsi1 grow used to build its target from the boot disk's live
// size, so only the first grow was ever charged, and a later boot-disk grow
// LOWERED the stored reservation. Each grow must now be charged on top of the
// effective footprint, and the reservation may only rise.
func TestResizeRepeatedDataDiskGrowIsCharged(t *testing.T) {
	var mu sync.Mutex
	sizes := map[string]int{"scsi0": 32, "scsi1": 10}
	var resizes int32
	mock := &proxmoxtest.MockClient{
		// PVE's maxdisk only ever reflects the boot disk.
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) {
			mu.Lock()
			defer mu.Unlock()
			return []proxmox.RawResource{
				{ID: "qemu/101", Type: "qemu", VMID: 101, Node: "pve01", MaxCPU: 2, MaxMem: 2048 << 20, MaxDisk: int64(sizes["scsi0"]) << 30},
			}, nil
		},
		OnGuestConfig: func(context.Context, proxmox.GuestRef) (map[string]any, error) {
			mu.Lock()
			defer mu.Unlock()
			return map[string]any{
				"scsi0": fmt.Sprintf("local-lvm:vm-101-disk-0,size=%dG", sizes["scsi0"]),
				"scsi1": fmt.Sprintf("local-lvm:vm-101-disk-1,size=%dG", sizes["scsi1"]),
			}, nil
		},
		// Model PVE applying each accepted resize immediately.
		OnResizeDisk: func(_ context.Context, _ proxmox.GuestRef, disk, size string) (proxmox.UPID, error) {
			atomic.AddInt32(&resizes, 1)
			n, err := strconv.Atoi(strings.TrimSuffix(size, "G"))
			if err != nil {
				t.Errorf("unexpected resize size %q", size)
			}
			mu.Lock()
			sizes[disk] = n
			mu.Unlock()
			return "UPID:pve01:0:0:0:resize:101:u@pam:", nil
		},
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	hh.fake.AddQuota("tenant", tenantA, nil, nil, qti64ptr(45), nil)
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userA)
	path := "/api/tenants/" + tenantA + "/guests/pve01/qemu/101/resize"
	reservedDisk := func() int64 {
		t.Helper()
		own, err := hh.fake.GetOwnershipByVMID(context.Background(), 101)
		if err != nil || own.ReservedDiskGB == nil {
			t.Fatalf("reserved_disk_gb unavailable (err %v)", err)
		}
		return *own.ReservedDiskGB
	}
	grow := func(body string, want int) {
		t.Helper()
		rec := hh.req(t, c, http.MethodPost, path, body)
		if rec.Code != want {
			t.Fatalf("resize %s = %d, want %d (body %s)", body, rec.Code, want, rec.Body.String())
		}
		if want == http.StatusConflict {
			if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "quota_exceeded" {
				t.Fatalf("resize %s error code = %q, want quota_exceeded", body, env.Error.Code)
			}
		}
	}

	// scsi1 10 → 20: +10 on top of the 32 GiB footprint = 42 ≤ 45.
	grow(`{"disk":"scsi1","sizeGib":20}`, http.StatusAccepted)
	if got := reservedDisk(); got != 42 {
		t.Fatalf("after first data-disk grow: reserved %d, want 42", got)
	}
	// scsi1 20 → 30: +10 on top of the effective 42 = 52 > 45. This grow used
	// to be free.
	grow(`{"disk":"scsi1","sizeGib":30}`, http.StatusConflict)
	// scsi0 32 → 33: +1 on top of 42 = 43 — the reservation RISES. The old code
	// persisted max(live 32, target 33) = 33, lowering it and reopening headroom.
	grow(`{"disk":"scsi0","sizeGib":33}`, http.StatusAccepted)
	if got := reservedDisk(); got != 43 {
		t.Fatalf("after boot-disk grow: reserved %d, want 43 (must never drop below 42)", got)
	}
	// The PoC's follow-up +39 grow is refused: 43 + 39 > 45.
	grow(`{"disk":"scsi1","sizeGib":59}`, http.StatusConflict)
	if n := atomic.LoadInt32(&resizes); n != 2 {
		t.Fatalf("ResizeDisk reached Proxmox %d times, want 2 (only the in-quota grows)", n)
	}
}

// TestReservationNeverLowered drives the fake store directly: a request whose
// target sits below the stored reservation is not a grow and must leave it
// alone, a direct lower write is ignored, and each disk grow stacks.
func TestReservationNeverLowered(t *testing.T) {
	ctx := context.Background()
	fake := storetest.New()
	tenantA := fake.AddTenant("A", "a")
	projA := fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	snap := map[int]store.Alloc{101: {VCPU: 2, RAMMB: 2048, DiskGB: 32}}
	reserve := func(p store.ReserveGrowthParams) {
		t.Helper()
		p.TenantID, p.ProjectID, p.VMID, p.Snapshot = tenantA, projA, 101, snap
		if err := fake.ReserveGuestGrowth(ctx, p); err != nil {
			t.Fatalf("ReserveGuestGrowth(%+v): %v", p, err)
		}
	}
	own := func() *store.ResourceOwnership {
		t.Helper()
		o, err := fake.GetOwnershipByVMID(ctx, 101)
		if err != nil {
			t.Fatalf("GetOwnershipByVMID: %v", err)
		}
		return o
	}

	reserve(store.ReserveGrowthParams{TargetVCPU: 6})
	reserve(store.ReserveGrowthParams{TargetVCPU: 4}) // below the reservation: not a grow
	if v := own().ReservedVCPU; v == nil || *v != 6 {
		t.Fatalf("reserved_vcpu = %v, want 6 (a lower target must not lower it)", v)
	}
	three := 3
	if err := fake.SetOwnershipReservation(ctx, tenantA, 101, &three, nil, nil); err != nil {
		t.Fatalf("SetOwnershipReservation: %v", err)
	}
	if v := own().ReservedVCPU; *v != 6 {
		t.Fatalf("reserved_vcpu = %d after a lower write, want 6", *v)
	}
	reserve(store.ReserveGrowthParams{GrowDiskGB: 5})
	reserve(store.ReserveGrowthParams{GrowDiskGB: 5})
	if d := own().ReservedDiskGB; d == nil || *d != 42 {
		t.Fatalf("reserved_disk_gb = %v, want 42 (32 + 5 + 5)", d)
	}
	if other := fake.AddTenant("B", "b"); fake.SetOwnershipReservation(ctx, other, 101, &three, nil, nil) == nil {
		t.Fatal("another tenant wrote this guest's reservation")
	}
}

// --- vCPU is sockets × cores, on config updates and rollbacks alike ---

// TestConfigGrowChargesSockets: quota counts PVE's maxcpu, which for a VM is
// sockets × cores. On a 2-socket guest, raising cores 2 → 4 adds 4 vCPU; the
// old code compared the requested cores (4) to maxcpu (4) and charged nothing.
func TestConfigGrowChargesSockets(t *testing.T) {
	var writes int32
	cfg := map[string]any{"sockets": float64(2), "cores": float64(2)}
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) {
			return []proxmox.RawResource{
				{ID: "qemu/101", Type: "qemu", VMID: 101, Node: "pve01", MaxCPU: 4, MaxMem: 2048 << 20, MaxDisk: 10 << 30},
			}, nil
		},
		OnGuestConfig: func(context.Context, proxmox.GuestRef) (map[string]any, error) { return cfg, nil },
		OnSetGuestConfig: func(context.Context, proxmox.GuestRef, map[string]any) (proxmox.UPID, error) {
			atomic.AddInt32(&writes, 1)
			return "UPID:pve01:0:0:0:qmconfig:101:u@pam:", nil
		},
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	hh.fake.AddQuota("tenant", tenantA, iptr(6), nil, nil, nil)
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userA)
	path := "/api/tenants/" + tenantA + "/guests/pve01/qemu/101/config"

	// 2 × 4 = 8 vCPU: +4 on 4 > 6.
	if rec := hh.req(t, c, http.MethodPatch, path, `{"cores":4}`); rec.Code != http.StatusConflict {
		t.Fatalf("cores 2→4 on 2 sockets = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if n := atomic.LoadInt32(&writes); n != 0 {
		t.Fatalf("SetGuestConfig reached Proxmox %d times on a refused grow", n)
	}
	// 2 × 3 = 6 vCPU: exactly at the cap.
	if rec := hh.req(t, c, http.MethodPatch, path, `{"cores":3}`); rec.Code != http.StatusAccepted {
		t.Fatalf("cores 2→3 on 2 sockets = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	own, err := hh.fake.GetOwnershipByVMID(context.Background(), 101)
	if err != nil || own.ReservedVCPU == nil || *own.ReservedVCPU != 6 {
		t.Fatalf("reserved_vcpu = %v (err %v), want 6", own.ReservedVCPU, err)
	}
	// A malformed sockets value fails closed.
	cfg["sockets"] = "two"
	if rec := hh.req(t, c, http.MethodPatch, path, `{"cores":3}`); rec.Code != http.StatusBadGateway {
		t.Fatalf("malformed sockets = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestRollbackFailsClosed: snapshot sizing that cannot be read refuses the
// rollback instead of letting it through ungated, and a VM snapshot's sockets
// count toward its vCPU.
func TestRollbackFailsClosed(t *testing.T) {
	var rollbacks int32
	snapConfigs := map[string]map[string]any{
		"garbled": {"cores": "lots", "memory": float64(2048)},
		"badmem":  {"cores": float64(2), "memory": "huge"},
		"sockets": {"cores": float64(2), "sockets": float64(2), "memory": float64(2048)}, // 4 vCPU
	}
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) {
			return []proxmox.RawResource{
				{ID: "qemu/101", Type: "qemu", VMID: 101, Node: "pve01", MaxCPU: 2, MaxMem: 2048 << 20, MaxDisk: 10 << 30},
			}, nil
		},
		OnSnapshotConfig: func(_ context.Context, _ proxmox.GuestRef, name string) (map[string]any, error) {
			return snapConfigs[name], nil
		},
		OnRollbackSnapshot: func(context.Context, proxmox.GuestRef, string) (proxmox.UPID, error) {
			atomic.AddInt32(&rollbacks, 1)
			return "UPID:pve01:0:0:0:qmrollback:101:u@pam:", nil
		},
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	hh.fake.AddQuota("tenant", tenantA, iptr(2), qti64ptr(2048), nil, nil) // caps == current size
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userA)
	base := "/api/tenants/" + tenantA + "/guests/pve01/qemu/101/snapshots/"

	for _, tc := range []struct {
		snap     string
		wantCode int
		wantErr  string
	}{
		{"garbled", http.StatusBadGateway, "proxmox_error"},
		{"badmem", http.StatusBadGateway, "proxmox_error"},
		// cores 2 matches today's maxcpu, but 2 sockets make it 4 vCPU — used to
		// pass ungated.
		{"sockets", http.StatusConflict, "quota_exceeded"},
	} {
		rec := hh.req(t, c, http.MethodPost, base+tc.snap+"/rollback", "")
		if rec.Code != tc.wantCode {
			t.Fatalf("rollback %s = %d, want %d (body %s)", tc.snap, rec.Code, tc.wantCode, rec.Body.String())
		}
		if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != tc.wantErr {
			t.Fatalf("rollback %s error code = %q, want %q", tc.snap, env.Error.Code, tc.wantErr)
		}
	}
	if n := atomic.LoadInt32(&rollbacks); n != 0 {
		t.Fatalf("RollbackSnapshot reached Proxmox %d times, want 0", n)
	}
}

// TestRollbackUnlimitedContainerRefused: a container snapshot without cores has
// no CPU limit at all — the container may use every host core — so its vCPU
// cannot be quota-checked and the rollback is refused.
func TestRollbackUnlimitedContainerRefused(t *testing.T) {
	var rollbacks int32
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) {
			return []proxmox.RawResource{
				{ID: "lxc/200", Type: "lxc", VMID: 200, Node: "pve01", MaxCPU: 2, MaxMem: 512 << 20, MaxDisk: 8 << 30},
			}, nil
		},
		OnSnapshotConfig: func(context.Context, proxmox.GuestRef, string) (map[string]any, error) {
			return map[string]any{"memory": float64(512)}, nil
		},
		OnRollbackSnapshot: func(context.Context, proxmox.GuestRef, string) (proxmox.UPID, error) {
			atomic.AddInt32(&rollbacks, 1)
			return "UPID:pve01:0:0:0:vzrollback:200:u@pam:", nil
		},
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	hh.fake.AddOwnership(tenantA, projA, 200, "lxc", "pve01", "active", nil)
	c := hh.cookie(t, userA)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/guests/pve01/lxc/200/snapshots/pre/rollback", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("unlimited-cores rollback = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "conflict" {
		t.Fatalf("error code = %q, want conflict", env.Error.Code)
	}
	if n := atomic.LoadInt32(&rollbacks); n != 0 {
		t.Fatalf("RollbackSnapshot reached Proxmox %d times, want 0", n)
	}
}
