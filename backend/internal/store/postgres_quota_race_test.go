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
