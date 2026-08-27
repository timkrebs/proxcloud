//go:build integration

package store

import (
	"context"
	"testing"
	"time"
)

// TestListStalePendingOwnership verifies the reconciler's sweep query against real
// Postgres: it returns ONLY pending rows created before the cutoff — never a fresh
// pending row and never an active row (however old).
func TestListStalePendingOwnership(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetQuotaTables(t, s)
	t.Cleanup(func() { resetQuotaTables(t, s) })
	ctx := context.Background()
	tenantID, p1, _ := seedTenantProject(t, s)

	mk := func(vmid int, status string, rv *int) *ResourceOwnership {
		o, err := s.CreateOwnership(ctx, CreateOwnershipParams{
			TenantID: tenantID, ProjectID: p1, VMID: vmid, GuestType: "lxc", Node: "pve01",
			Status: status, ReservedVCPU: rv, ReservedRAMMB: i64ptr(1024), ReservedDiskGB: i64ptr(20),
		})
		if err != nil {
			t.Fatalf("CreateOwnership %d: %v", vmid, err)
		}
		return o
	}
	backdate := func(id string) {
		if _, err := s.pool.Exec(ctx, `UPDATE resource_ownership SET created_at = now() - interval '2 hours' WHERE id = $1::uuid`, id); err != nil {
			t.Fatalf("backdate %s: %v", id, err)
		}
	}

	stale := mk(400, "pending", iptr(2)) // old + pending → returned
	mk(401, "pending", iptr(1))          // fresh + pending → excluded
	oldActive := mk(402, "active", nil)  // old + active → excluded (not a reservation)
	backdate(stale.ID)
	backdate(oldActive.ID)

	cutoff := time.Now().Add(-1 * time.Hour)
	rows, err := s.ListStalePendingOwnership(ctx, cutoff)
	if err != nil {
		t.Fatalf("ListStalePendingOwnership: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("stale pending = %d rows, want 1 (only VMID 400)", len(rows))
	}
	if rows[0].VMID != 400 || rows[0].Status != "pending" {
		t.Fatalf("stale row = %+v, want VMID 400 pending", rows[0])
	}
	if rows[0].ReservedVCPU == nil || *rows[0].ReservedVCPU != 2 {
		t.Fatalf("stale row reserved_vcpu = %v, want 2 (carried for quota accounting)", rows[0].ReservedVCPU)
	}
}
