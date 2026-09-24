//go:build integration

package store

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestReserveOwnershipRaceRespectsCap is the concurrency gate the fake's no-op
// AdvisoryLock cannot prove (mirrors TestBootstrapRaceGuard): N parallel
// ReserveOwnership in one tenant with max_count=M must let EXACTLY M succeed and
// refuse the other N−M with ErrQuotaExceeded. The per-tenant advisory lock in
// ReserveOwnership serializes the read-modify-write so the cap is never breached.
func TestReserveOwnershipRaceRespectsCap(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetQuotaTables(t, s)
	t.Cleanup(func() { resetQuotaTables(t, s) })
	ctx := context.Background()
	tenantID, p1, _ := seedTenantProject(t, s)

	const (
		cap        = 5
		goroutines = 20
	)
	if _, err := s.UpsertQuota(ctx, UpsertQuotaParams{ScopeType: "tenant", ScopeID: tenantID, MaxCount: iptr(cap)}); err != nil {
		t.Fatalf("UpsertQuota: %v", err)
	}

	var (
		wg        sync.WaitGroup
		successes int64
		quotaHits int64
	)
	snap := map[int]Alloc{} // no active guests; the cap binds on pending count
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			_, err := s.ReserveOwnership(cctx, ReserveOwnershipParams{
				TenantID: tenantID, ProjectID: p1, VMID: 1000 + i, GuestType: "lxc", Node: "pve01",
				Reserved: Alloc{VCPU: 1, RAMMB: 128, DiskGB: 1}, Snapshot: snap,
			})
			var qe ErrQuotaExceeded
			switch {
			case err == nil:
				atomic.AddInt64(&successes, 1)
			case errors.As(err, &qe):
				atomic.AddInt64(&quotaHits, 1)
			default:
				t.Errorf("goroutine %d: unexpected error: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if successes != cap {
		t.Fatalf("reservations succeeded %d times, want exactly %d (cap)", successes, cap)
	}
	if quotaHits != goroutines-cap {
		t.Fatalf("quota refusals = %d, want %d", quotaHits, goroutines-cap)
	}
	// The DB agrees: exactly cap pending rows exist for the tenant.
	tenantUsage, _, err := s.ComputeUsage(ctx, tenantID, snap)
	if err != nil {
		t.Fatalf("ComputeUsage: %v", err)
	}
	if tenantUsage.Count != cap {
		t.Fatalf("pending rows = %d, want %d (no over-commit)", tenantUsage.Count, cap)
	}
}

// TestReserveOwnershipTenantLockAcrossProjects proves the lock is keyed on the
// TENANT, not the project (ADR-0012 §1): two projects of one tenant racing the
// tenant cap must, together, never exceed it. A per-project lock would let each
// project pass the tenant check independently and blow the cap.
func TestReserveOwnershipTenantLockAcrossProjects(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetQuotaTables(t, s)
	t.Cleanup(func() { resetQuotaTables(t, s) })
	ctx := context.Background()
	tenantID, p1, p2 := seedTenantProject(t, s)

	const (
		cap        = 5
		goroutines = 20
	)
	// Tenant cap only; no project caps, so the tenant lock is the sole guard.
	if _, err := s.UpsertQuota(ctx, UpsertQuotaParams{ScopeType: "tenant", ScopeID: tenantID, MaxCount: iptr(cap)}); err != nil {
		t.Fatalf("UpsertQuota: %v", err)
	}

	var (
		wg        sync.WaitGroup
		successes int64
	)
	snap := map[int]Alloc{}
	for i := 0; i < goroutines; i++ {
		project := p1
		if i%2 == 1 {
			project = p2 // alternate projects to race the cross-project path
		}
		wg.Add(1)
		go func(i int, project string) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			_, err := s.ReserveOwnership(cctx, ReserveOwnershipParams{
				TenantID: tenantID, ProjectID: project, VMID: 2000 + i, GuestType: "lxc", Node: "pve01",
				Reserved: Alloc{VCPU: 1, RAMMB: 128, DiskGB: 1}, Snapshot: snap,
			})
			var qe ErrQuotaExceeded
			switch {
			case err == nil:
				atomic.AddInt64(&successes, 1)
			case errors.As(err, &qe):
				// expected for the losers
			default:
				t.Errorf("goroutine %d: unexpected error: %v", i, err)
			}
		}(i, project)
	}
	wg.Wait()

	if successes != cap {
		t.Fatalf("cross-project reservations succeeded %d times, want exactly %d (tenant cap)", successes, cap)
	}
	tenantUsage, _, err := s.ComputeUsage(ctx, tenantID, snap)
	if err != nil {
		t.Fatalf("ComputeUsage: %v", err)
	}
	if tenantUsage.Count != cap {
		t.Fatalf("total pending across both projects = %d, want %d (tenant lock held)", tenantUsage.Count, cap)
	}
}

// TestReserveGuestGrowthRaceRespectsCap is the M-a concurrency gate the fake's
// mutex cannot prove against real Postgres: N parallel ReserveGuestGrowth calls
// racing the SAME headroom must let exactly the headroom's worth succeed. Ten
// active guests at 2 vCPU (usage 20) under a 24-vCPU cap leave room for ONE
// +4 grow; each goroutine grows its own guest by 4, so exactly one wins — the
// per-tenant advisory lock serializes the check AND the same-transaction
// reservation write, closing the old check-then-act window.
func TestReserveGuestGrowthRaceRespectsCap(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetQuotaTables(t, s)
	t.Cleanup(func() { resetQuotaTables(t, s) })
	ctx := context.Background()
	tenantID, p1, _ := seedTenantProject(t, s)

	const guests = 10
	snap := map[int]Alloc{}
	for i := 0; i < guests; i++ {
		vmid := 3000 + i
		if _, err := s.CreateOwnership(ctx, CreateOwnershipParams{
			TenantID: tenantID, ProjectID: p1, VMID: vmid, GuestType: "qemu", Node: "pve01", Status: "active",
		}); err != nil {
			t.Fatalf("CreateOwnership vmid %d: %v", vmid, err)
		}
		snap[vmid] = Alloc{VCPU: 2, RAMMB: 512, DiskGB: 8}
	}
	// usage 20 vCPU; cap 24 → headroom 4 = exactly one +4 grow.
	if _, err := s.UpsertQuota(ctx, UpsertQuotaParams{ScopeType: "tenant", ScopeID: tenantID, MaxVCPU: iptr(24)}); err != nil {
		t.Fatalf("UpsertQuota: %v", err)
	}

	var (
		wg        sync.WaitGroup
		successes int64
		quotaHits int64
	)
	for i := 0; i < guests; i++ {
		wg.Add(1)
		go func(vmid int) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			defer cancel()
			err := s.ReserveGuestGrowth(cctx, ReserveGrowthParams{
				TenantID: tenantID, ProjectID: p1, VMID: vmid, Snapshot: snap,
				TargetVCPU: 6, // +4 each; the cap admits one
			})
			var qe ErrQuotaExceeded
			switch {
			case err == nil:
				atomic.AddInt64(&successes, 1)
			case errors.As(err, &qe):
				atomic.AddInt64(&quotaHits, 1)
			default:
				t.Errorf("vmid %d: unexpected error: %v", vmid, err)
			}
		}(3000 + i)
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("grows succeeded %d times, want exactly 1 (headroom is 4 vCPU)", successes)
	}
	if quotaHits != guests-1 {
		t.Fatalf("quota refusals = %d, want %d", quotaHits, guests-1)
	}
	// The DB agrees: usage counts the single reservation, never past the cap.
	tenantUsage, _, err := s.ComputeUsage(ctx, tenantID, snap)
	if err != nil {
		t.Fatalf("ComputeUsage: %v", err)
	}
	if tenantUsage.VCPU != 24 {
		t.Fatalf("usage after race = %d vCPU, want 24 (9 guests at live 2 + the winner's reserved footprint 6)", tenantUsage.VCPU)
	}
}

// TestGrowthReservationMonotonicSQL pins the SQL half of the disk-quota bypass
// fix against real Postgres: GREATEST ignores NULLs, so a nil dimension stays
// untouched, an absent reservation takes the new value, and a lower value never
// lowers a stored one; the reads and writes are tenant-filtered in SQL; and a
// repeated disk grow is charged each time on top of the prior reservation.
func TestGrowthReservationMonotonicSQL(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetQuotaTables(t, s)
	t.Cleanup(func() { resetQuotaTables(t, s) })
	ctx := context.Background()
	tenantID, p1, _ := seedTenantProject(t, s)
	const vmid = 3100
	if _, err := s.CreateOwnership(ctx, CreateOwnershipParams{
		TenantID: tenantID, ProjectID: p1, VMID: vmid, GuestType: "qemu", Node: "pve01", Status: "active",
	}); err != nil {
		t.Fatalf("CreateOwnership: %v", err)
	}
	read := func() *ResourceOwnership {
		t.Helper()
		o, err := s.GetLiveOwnershipForTenant(ctx, tenantID, vmid)
		if err != nil {
			t.Fatalf("GetLiveOwnershipForTenant: %v", err)
		}
		return o
	}

	// Absent reservation takes the value; nil dimensions stay NULL.
	v6 := 6
	if err := s.SetOwnershipReservation(ctx, tenantID, vmid, &v6, nil, nil); err != nil {
		t.Fatalf("SetOwnershipReservation: %v", err)
	}
	if o := read(); o.ReservedVCPU == nil || *o.ReservedVCPU != 6 || o.ReservedRAMMB != nil || o.ReservedDiskGB != nil {
		t.Fatalf("after first write: vcpu=%v ram=%v disk=%v, want 6/nil/nil", o.ReservedVCPU, o.ReservedRAMMB, o.ReservedDiskGB)
	}

	// A lower value never lowers; a new dimension is still written.
	v3, r4096 := 3, int64(4096)
	if err := s.SetOwnershipReservation(ctx, tenantID, vmid, &v3, &r4096, nil); err != nil {
		t.Fatalf("SetOwnershipReservation (lower): %v", err)
	}
	if o := read(); *o.ReservedVCPU != 6 || o.ReservedRAMMB == nil || *o.ReservedRAMMB != 4096 {
		t.Fatalf("after lower write: vcpu=%v ram=%v, want 6 (unchanged) / 4096", o.ReservedVCPU, o.ReservedRAMMB)
	}

	// Another tenant can neither read nor write the row.
	const foreign = "00000000-0000-0000-0000-000000000000"
	if _, err := s.GetLiveOwnershipForTenant(ctx, foreign, vmid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign read err = %v, want ErrNotFound", err)
	}
	v99 := 99
	if err := s.SetOwnershipReservation(ctx, foreign, vmid, &v99, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign write err = %v, want ErrNotFound", err)
	}
	if o := read(); *o.ReservedVCPU != 6 {
		t.Fatalf("foreign write leaked: vcpu=%d, want 6", *o.ReservedVCPU)
	}

	// Each disk grow is charged on top of the previous reservation.
	snap := map[int]Alloc{vmid: {VCPU: 2, RAMMB: 2048, DiskGB: 32}}
	for i, want := range []int64{42, 52} {
		if err := s.ReserveGuestGrowth(ctx, ReserveGrowthParams{
			TenantID: tenantID, ProjectID: p1, VMID: vmid, Snapshot: snap, GrowDiskGB: 10,
		}); err != nil {
			t.Fatalf("disk grow %d: %v", i+1, err)
		}
		if o := read(); o.ReservedDiskGB == nil || *o.ReservedDiskGB != want {
			t.Fatalf("after disk grow %d: reserved_disk_gb=%v, want %d", i+1, o.ReservedDiskGB, want)
		}
	}
}
