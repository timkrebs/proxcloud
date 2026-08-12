package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// resetQuotaTables clears the aggregates the Phase-4 quota/audit integration
// tests touch, on top of resetPhase3Tables (which keeps the seeded default
// tenant/project). Guarded against non-ephemeral databases via guardDestructive.
func resetQuotaTables(t *testing.T, s *PgStore) {
	t.Helper()
	guardDestructive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, q := range []string{`DELETE FROM audit_log`, `DELETE FROM quotas`} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatalf("reset quota (%s): %v", q, err)
		}
	}
	resetPhase3Tables(t, s)
}

// seedTenantProject creates a tenant with two projects and returns their ids.
func seedTenantProject(t *testing.T, s *PgStore) (tenantID, proj1, proj2 string) {
	t.Helper()
	ctx := context.Background()
	ten, err := s.CreateTenant(ctx, CreateTenantParams{Name: "Acme", Slug: "acme"})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	p1, err := s.CreateProject(ctx, CreateProjectParams{TenantID: ten.ID, Name: "P1", Slug: "p1", PoolID: "pc-acme-p1"})
	if err != nil {
		t.Fatalf("CreateProject p1: %v", err)
	}
	p2, err := s.CreateProject(ctx, CreateProjectParams{TenantID: ten.ID, Name: "P2", Slug: "p2", PoolID: "pc-acme-p2"})
	if err != nil {
		t.Fatalf("CreateProject p2: %v", err)
	}
	return ten.ID, p1.ID, p2.ID
}

func TestUpsertAndGetQuota(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetQuotaTables(t, s)
	t.Cleanup(func() { resetQuotaTables(t, s) })
	ctx := context.Background()
	tenantID, _, _ := seedTenantProject(t, s)

	// Absent → ErrNotFound (caller treats as unlimited).
	if _, err := s.GetQuota(ctx, "tenant", tenantID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetQuota(absent) = %v, want ErrNotFound", err)
	}

	// Insert.
	q, err := s.UpsertQuota(ctx, UpsertQuotaParams{ScopeType: "tenant", ScopeID: tenantID, MaxVCPU: iptr(8), MaxRAMMB: i64ptr(4096)})
	if err != nil {
		t.Fatalf("UpsertQuota insert: %v", err)
	}
	if q.MaxVCPU == nil || *q.MaxVCPU != 8 || q.MaxDiskGB != nil {
		t.Fatalf("insert quota = %+v", q)
	}

	// Upsert replaces (ON CONFLICT DO UPDATE): clears vcpu, sets disk.
	q2, err := s.UpsertQuota(ctx, UpsertQuotaParams{ScopeType: "tenant", ScopeID: tenantID, MaxDiskGB: i64ptr(500)})
	if err != nil {
		t.Fatalf("UpsertQuota update: %v", err)
	}
	if q2.ID != q.ID {
		t.Fatalf("upsert created a new row (%s != %s)", q2.ID, q.ID)
	}
	if q2.MaxVCPU != nil || q2.MaxDiskGB == nil || *q2.MaxDiskGB != 500 {
		t.Fatalf("upsert quota = %+v, want vcpu cleared, disk 500", q2)
	}
}

func TestComputeUsageAggregation(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetQuotaTables(t, s)
	t.Cleanup(func() { resetQuotaTables(t, s) })
	ctx := context.Background()
	tenantID, p1, p2 := seedTenantProject(t, s)

	// Active guest 101 in P1; pending reservation 200 in P1; active 102 in P2;
	// active 103 in P2 but ABSENT from the snapshot (deleted/not-yet-visible).
	mustOwn := func(pid string, vmid int, status string, rv *int, rr, rd *int64) {
		if _, err := s.CreateOwnership(ctx, CreateOwnershipParams{
			TenantID: tenantID, ProjectID: pid, VMID: vmid, GuestType: "qemu", Node: "pve01",
			Status: status, ReservedVCPU: rv, ReservedRAMMB: rr, ReservedDiskGB: rd,
		}); err != nil {
			t.Fatalf("CreateOwnership %d: %v", vmid, err)
		}
	}
	mustOwn(p1, 101, "active", nil, nil, nil)
	mustOwn(p1, 200, "pending", iptr(2), i64ptr(1024), i64ptr(20))
	mustOwn(p2, 102, "active", nil, nil, nil)
	mustOwn(p2, 103, "active", nil, nil, nil)

	snapshot := map[int]Alloc{
		101: {VCPU: 4, RAMMB: 2048, DiskGB: 40},
		102: {VCPU: 1, RAMMB: 512, DiskGB: 10},
		// 103 intentionally absent → contributes 0, not counted.
	}
	tenant, byProject, err := s.ComputeUsage(ctx, tenantID, snapshot)
	if err != nil {
		t.Fatalf("ComputeUsage: %v", err)
	}

	wantP1 := QuotaUsage{VCPU: 6, RAMMB: 3072, DiskGB: 60, Count: 2} // active 4/2048/40 + pending 2/1024/20
	wantP2 := QuotaUsage{VCPU: 1, RAMMB: 512, DiskGB: 10, Count: 1}  // 103 excluded
	wantTenant := QuotaUsage{VCPU: 7, RAMMB: 3584, DiskGB: 70, Count: 3}
	if byProject[p1] != wantP1 {
		t.Fatalf("P1 usage = %+v, want %+v", byProject[p1], wantP1)
	}
	if byProject[p2] != wantP2 {
		t.Fatalf("P2 usage = %+v, want %+v", byProject[p2], wantP2)
	}
	if tenant != wantTenant {
		t.Fatalf("tenant usage = %+v, want %+v", tenant, wantTenant)
	}
}

func TestReserveOwnership(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetQuotaTables(t, s)
	t.Cleanup(func() { resetQuotaTables(t, s) })
	ctx := context.Background()
	tenantID, p1, _ := seedTenantProject(t, s)

	// Happy path: tenant vCPU cap 8, one active guest using 4 (snapshot); a
	// reservation of 4 exactly fits and inserts a pending row with reserved_* set.
	if _, err := s.UpsertQuota(ctx, UpsertQuotaParams{ScopeType: "tenant", ScopeID: tenantID, MaxVCPU: iptr(8)}); err != nil {
		t.Fatalf("UpsertQuota tenant: %v", err)
	}
	if _, err := s.CreateOwnership(ctx, CreateOwnershipParams{TenantID: tenantID, ProjectID: p1, VMID: 101, GuestType: "qemu", Node: "pve01", Status: "active"}); err != nil {
		t.Fatalf("seed active: %v", err)
	}
	snap := map[int]Alloc{101: {VCPU: 4}}
	own, err := s.ReserveOwnership(ctx, ReserveOwnershipParams{
		TenantID: tenantID, ProjectID: p1, VMID: 300, GuestType: "qemu", Node: "pve01",
		Reserved: Alloc{VCPU: 4, RAMMB: 1024, DiskGB: 20}, Snapshot: snap,
	})
	if err != nil {
		t.Fatalf("ReserveOwnership happy: %v", err)
	}
	if own.Status != "pending" || own.ReservedVCPU == nil || *own.ReservedVCPU != 4 {
		t.Fatalf("reserved row = %+v, want pending with reserved_vcpu 4", own)
	}

	// A second reservation of 1 more vCPU would make 4+4+1 > 8 → tenant vcpu.
	_, err = s.ReserveOwnership(ctx, ReserveOwnershipParams{
		TenantID: tenantID, ProjectID: p1, VMID: 301, GuestType: "qemu", Node: "pve01",
		Reserved: Alloc{VCPU: 1}, Snapshot: snap,
	})
	var qe ErrQuotaExceeded
	if !errors.As(err, &qe) || qe.Scope != "tenant" || qe.Dimension != "vcpu" {
		t.Fatalf("over-tenant-cap reservation = %v, want ErrQuotaExceeded tenant vcpu", err)
	}
	// The rejected reservation left no row.
	if _, err := s.GetOwnershipByVMID(ctx, 301); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected reservation leaked a row: %v", err)
	}

	// Project cap (RAM) is enforced independently of the tenant cap.
	if _, err := s.UpsertQuota(ctx, UpsertQuotaParams{ScopeType: "project", ScopeID: p1, MaxRAMMB: i64ptr(2048)}); err != nil {
		t.Fatalf("UpsertQuota project: %v", err)
	}
	_, err = s.ReserveOwnership(ctx, ReserveOwnershipParams{
		TenantID: tenantID, ProjectID: p1, VMID: 302, GuestType: "qemu", Node: "pve01",
		Reserved: Alloc{RAMMB: 2048}, Snapshot: snap, // pending 300 used 1024; +2048 > 2048
	})
	if !errors.As(err, &qe) || qe.Scope != "project" || qe.Dimension != "ram_mb" {
		t.Fatalf("over-project-cap reservation = %v, want ErrQuotaExceeded project ram_mb", err)
	}

	// Duplicate VMID → ErrConflict (unique clash), never a quota error.
	_, err = s.ReserveOwnership(ctx, ReserveOwnershipParams{
		TenantID: tenantID, ProjectID: p1, VMID: 101, GuestType: "qemu", Node: "pve01",
		Reserved: Alloc{VCPU: 0}, Snapshot: snap,
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate VMID reservation = %v, want ErrConflict", err)
	}
}

func TestAuditRoundTripTenantFiltered(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetQuotaTables(t, s)
	t.Cleanup(func() { resetQuotaTables(t, s) })
	ctx := context.Background()

	tenA, projA, _ := seedTenantProject(t, s)
	tenB, err := s.CreateTenant(ctx, CreateTenantParams{Name: "Beta", Slug: "beta"})
	if err != nil {
		t.Fatalf("CreateTenant B: %v", err)
	}

	// Intent for tenant A, then finalize with an outcome + detail.
	id, err := s.InsertAuditIntent(ctx, AuditIntent{
		TenantID: &tenA, ProjectID: &projA, Action: "project.quota.update",
		TargetType: sptr("project"), TargetID: &projA,
	})
	if err != nil {
		t.Fatalf("InsertAuditIntent: %v", err)
	}
	detail := []byte(`{"status":200}`)
	if err := s.FinalizeAudit(ctx, id, "success", detail); err != nil {
		t.Fatalf("FinalizeAudit: %v", err)
	}
	// A second, tenant-B row that must never appear in tenant-A's list.
	if _, err := s.InsertAuditIntent(ctx, AuditIntent{TenantID: &tenB.ID, Action: "guest.create"}); err != nil {
		t.Fatalf("InsertAuditIntent B: %v", err)
	}

	got, err := s.ListAudit(ctx, AuditQuery{TenantID: tenA, Limit: 50})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("ListAudit(A) returned %d rows, want 1 (tenant filter)", len(got))
	}
	e := got[0]
	// jsonb normalizes whitespace, so compare the decoded value, not the bytes.
	var gotDetail map[string]int
	if err := json.Unmarshal(e.Detail, &gotDetail); err != nil {
		t.Fatalf("audit detail is not the stored json: %q (%v)", e.Detail, err)
	}
	if e.ID != id || e.Outcome != "success" || gotDetail["status"] != 200 {
		t.Fatalf("finalized row = %+v, want id %s outcome success detail %s", e, id, detail)
	}
	if e.Action != "project.quota.update" || e.ProjectID == nil || *e.ProjectID != projA {
		t.Fatalf("row fields = %+v", e)
	}

	// Outcome filter that matches nothing → empty (not a leak).
	if rows, _ := s.ListAudit(ctx, AuditQuery{TenantID: tenA, Outcome: "denied", Limit: 50}); len(rows) != 0 {
		t.Fatalf("outcome filter returned %d, want 0", len(rows))
	}
	// FinalizeAudit on a missing id → ErrNotFound.
	if err := s.FinalizeAudit(ctx, "00000000-0000-0000-0000-000000000000", "success", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FinalizeAudit(missing) = %v, want ErrNotFound", err)
	}
}

func sptr(s string) *string { return &s }
