package reconciler_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/timkrebs9/proxcloud/backend/internal/reconciler"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

func iptr(v int) *int { return &v }

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newReconciler(fake *storetest.Fake, now time.Time, ttl time.Duration) *reconciler.Reconciler {
	return &reconciler.Reconciler{
		Store:    fake,
		Log:      discardLog(),
		Interval: time.Minute,
		TTL:      ttl,
		Now:      func() time.Time { return now },
	}
}

// --- a stale pending row is released + audited; a fresh one is left alone ---

func TestReconcilerReclaimsStaleReservations(t *testing.T) {
	fake := storetest.New()
	now := time.Now()
	tenantA := fake.AddTenant("A", "a")
	projA := fake.AddProject(tenantA, "Web", "web", "pc-a-web")

	// Stale reservation: created 60m ago (older than the 45m TTL).
	fake.Now = func() time.Time { return now.Add(-60 * time.Minute) }
	fake.AddPendingReservation(tenantA, projA, 200, "lxc", "pve01", 2, 1024, 20)
	// Fresh reservation: created just now.
	fake.Now = func() time.Time { return now }
	fake.AddPendingReservation(tenantA, projA, 201, "lxc", "pve01", 2, 1024, 20)

	rc := newReconciler(fake, now, 45*time.Minute)
	if n := rc.Sweep(context.Background()); n != 1 {
		t.Fatalf("Sweep reclaimed %d, want 1", n)
	}

	if s := fake.OwnershipStatus(200); s != "" {
		t.Fatalf("stale reservation status = %q, want released (gone)", s)
	}
	if s := fake.OwnershipStatus(201); s != "pending" {
		t.Fatalf("fresh reservation status = %q, want pending (untouched)", s)
	}

	rows, err := fake.ListAudit(context.Background(), store.AuditQuery{TenantID: tenantA, Limit: 100})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1 (one reclaim)", len(rows))
	}
	e := rows[0]
	if e.Action != "reservation.reclaimed" || e.Outcome != "success" {
		t.Fatalf("audit row = action %q outcome %q, want reservation.reclaimed/success", e.Action, e.Outcome)
	}
	if e.ActorUserID != nil {
		t.Fatalf("reclaim actor = %v, want nil (system)", *e.ActorUserID)
	}
	if e.TargetID == nil || *e.TargetID != "200" {
		t.Fatalf("reclaim target = %v, want 200", e.TargetID)
	}
}

// --- reclaiming frees the quota the stale reservation was holding ---

func TestReconcilerReclaimFreesQuota(t *testing.T) {
	fake := storetest.New()
	now := time.Now()
	ctx := context.Background()
	tenantA := fake.AddTenant("A", "a")
	projA := fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	fake.AddQuota("tenant", tenantA, iptr(4), nil, nil, nil) // MaxVCPU=4

	// A stale pending reservation is holding the whole 4-vCPU cap.
	fake.Now = func() time.Time { return now.Add(-60 * time.Minute) }
	fake.AddPendingReservation(tenantA, projA, 200, "lxc", "pve01", 4, 1024, 20)
	fake.Now = func() time.Time { return now }

	snap := map[int]store.Alloc{}
	// Before reclaim: a 1-vCPU reserve is over cap (4 + 1 > 4).
	_, err := fake.ReserveOwnership(ctx, store.ReserveOwnershipParams{
		TenantID: tenantA, ProjectID: projA, VMID: 300, GuestType: "lxc", Node: "pve01",
		Reserved: store.Alloc{VCPU: 1}, Snapshot: snap,
	})
	var qe store.ErrQuotaExceeded
	if !errors.As(err, &qe) {
		t.Fatalf("pre-reclaim reserve = %v, want ErrQuotaExceeded", err)
	}

	// Reclaim the stale reservation, freeing its 4 vCPU.
	if n := newReconciler(fake, now, 45*time.Minute).Sweep(ctx); n != 1 {
		t.Fatalf("Sweep reclaimed %d, want 1", n)
	}

	// The same reserve now fits.
	if _, err := fake.ReserveOwnership(ctx, store.ReserveOwnershipParams{
		TenantID: tenantA, ProjectID: projA, VMID: 301, GuestType: "lxc", Node: "pve01",
		Reserved: store.Alloc{VCPU: 1}, Snapshot: snap,
	}); err != nil {
		t.Fatalf("post-reclaim reserve = %v, want success (quota freed)", err)
	}
}

// --- a fail-closed intent insert leaves the reservation in place (no silent free) ---

func TestReconcilerIntentFailureKeepsReservation(t *testing.T) {
	fake := storetest.New()
	now := time.Now()
	tenantA := fake.AddTenant("A", "a")
	projA := fake.AddProject(tenantA, "Web", "web", "pc-a-web")

	fake.Now = func() time.Time { return now.Add(-60 * time.Minute) }
	fake.AddPendingReservation(tenantA, projA, 200, "lxc", "pve01", 2, 1024, 20)
	fake.Now = func() time.Time { return now }
	fake.FailOn("InsertAuditIntent", errors.New("audit db down"))

	if n := newReconciler(fake, now, 45*time.Minute).Sweep(context.Background()); n != 0 {
		t.Fatalf("Sweep reclaimed %d, want 0 (intent failed → no release)", n)
	}
	if s := fake.OwnershipStatus(200); s != "pending" {
		t.Fatalf("reservation status = %q, want still pending (not freed without an audit trail)", s)
	}
}
