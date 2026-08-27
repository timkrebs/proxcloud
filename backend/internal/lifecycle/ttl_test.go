package lifecycle

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	pmx "github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

// seedTTL upserts a TTL for a guest and returns the stored row.
func seedTTL(t *testing.T, f *storetest.Fake, tid, pid string, vmid int, action string, expiresAt time.Time, original time.Duration) *store.TTL {
	t.Helper()
	ttl, err := f.UpsertTTL(context.Background(), store.UpsertTTLParams{
		TenantID: tid, ProjectID: pid, VMID: vmid, Action: action,
		ExpiresAt: expiresAt, OriginalDuration: original,
	})
	if err != nil {
		t.Fatalf("seed ttl: %v", err)
	}
	return ttl
}

func ttlJobFor(handler string, vmid int, tid, pid string, pl ttlJobPayload) store.Job {
	b, _ := json.Marshal(pl)
	return store.Job{
		ID: "job-ttl", Kind: "one_shot", Handler: handler,
		VMID: &vmid, TenantID: &tid, ProjectID: &pid, Payload: b, Status: "running",
	}
}

// --- materialization ---

func TestTTLMaterializeEmitsThreeJobs(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 301)
	now := fixedNow()()
	expiry := now.Add(72 * time.Hour)
	seedTTL(t, f, tid, pid, 301, "delete", expiry, 72*time.Hour)

	own, _ := f.GetOwnershipByVMID(ctx, 301)
	svc := &TTL{Store: f, Log: discardLog(), Now: fixedNow()}
	if err := svc.MaterializeForGuest(ctx, *own); err != nil {
		t.Fatalf("MaterializeForGuest: %v", err)
	}

	byHandler := map[string][]store.Job{}
	for _, j := range f.AllJobs() {
		byHandler[j.Handler] = append(byHandler[j.Handler], j)
	}
	if len(byHandler[HandlerTTLWarn]) != 2 {
		t.Fatalf("emitted %d warn jobs, want 2", len(byHandler[HandlerTTLWarn]))
	}
	if len(byHandler[HandlerTTLExpire]) != 1 {
		t.Fatalf("emitted %d expire jobs, want 1", len(byHandler[HandlerTTLExpire]))
	}
	for _, j := range f.AllJobs() {
		if j.Kind != "one_shot" {
			t.Errorf("%s kind = %q, want one_shot", j.Handler, j.Kind)
		}
		if j.MissedPolicy != "run_late" {
			t.Errorf("%s missed_policy = %q, want run_late", j.Handler, j.MissedPolicy)
		}
	}
	// Warn run_at values are expiry-24h and expiry-1h; expire fires exactly at expiry.
	expWarn := map[time.Time]bool{expiry.Add(-24 * time.Hour): false, expiry.Add(-1 * time.Hour): false}
	for _, j := range byHandler[HandlerTTLWarn] {
		if _, ok := expWarn[j.RunAt]; !ok {
			t.Errorf("unexpected warn run_at %v", j.RunAt)
		}
		expWarn[j.RunAt] = true
	}
	for at, seen := range expWarn {
		if !seen {
			t.Errorf("no warn job scheduled at %v", at)
		}
	}
	if got := byHandler[HandlerTTLExpire][0].RunAt; !got.Equal(expiry) {
		t.Errorf("expire run_at = %v, want %v", got, expiry)
	}
}

func TestTTLMaterializeSkipsPastAndFlaggedWarns(t *testing.T) {
	ctx := context.Background()

	t.Run("expiry within an hour skips both warns", func(t *testing.T) {
		f := storetest.New()
		tid, pid := seedGuest(f, 302)
		now := fixedNow()()
		seedTTL(t, f, tid, pid, 302, "stop", now.Add(30*time.Minute), 30*time.Minute)
		own, _ := f.GetOwnershipByVMID(ctx, 302)
		svc := &TTL{Store: f, Log: discardLog(), Now: fixedNow()}
		if err := svc.MaterializeForGuest(ctx, *own); err != nil {
			t.Fatalf("MaterializeForGuest: %v", err)
		}
		for _, j := range f.AllJobs() {
			if j.Handler == HandlerTTLWarn {
				t.Fatalf("emitted a warn job whose tier is already in the past")
			}
		}
		if n := len(f.AllJobs()); n != 1 {
			t.Fatalf("emitted %d jobs, want only the expire job", n)
		}
	})

	t.Run("already-flagged 24h warn is skipped", func(t *testing.T) {
		f := storetest.New()
		tid, pid := seedGuest(f, 303)
		now := fixedNow()()
		seedTTL(t, f, tid, pid, 303, "delete", now.Add(72*time.Hour), 72*time.Hour)
		if err := f.SetTTLWarned(ctx, 303, "24h"); err != nil {
			t.Fatalf("set warned: %v", err)
		}
		own, _ := f.GetOwnershipByVMID(ctx, 303)
		svc := &TTL{Store: f, Log: discardLog(), Now: fixedNow()}
		if err := svc.MaterializeForGuest(ctx, *own); err != nil {
			t.Fatalf("MaterializeForGuest: %v", err)
		}
		warns := 0
		for _, j := range f.AllJobs() {
			if j.Handler == HandlerTTLWarn {
				warns++
			}
		}
		if warns != 1 {
			t.Fatalf("emitted %d warn jobs, want 1 (24h already flagged)", warns)
		}
	})
}

// TestTTLMaterializePreservesAutoShutdownJobs proves TTL re-materialization
// cancels only ttl.* jobs, never a guest's auto-shutdown jobs (ADR-0020).
func TestTTLMaterializePreservesAutoShutdownJobs(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 304)
	as, _ := f.EnqueueJob(ctx, store.EnqueueJobParams{
		Kind: "recurring", Handler: HandlerStop, TenantID: &tid, ProjectID: &pid, VMID: intptr(304),
		RunAt: fixedNow()(),
	})
	seedTTL(t, f, tid, pid, 304, "stop", fixedNow()().Add(48*time.Hour), 48*time.Hour)
	own, _ := f.GetOwnershipByVMID(ctx, 304)
	svc := &TTL{Store: f, Log: discardLog(), Now: fixedNow()}
	if err := svc.MaterializeForGuest(ctx, *own); err != nil {
		t.Fatalf("MaterializeForGuest: %v", err)
	}
	if got := f.JobStatus(as.ID); got != "scheduled" {
		t.Fatalf("auto-shutdown job status = %q, want scheduled (untouched)", got)
	}
}

// --- warn handler ---

func TestTTLWarn(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 310)
	expiry := fixedNow()().Add(24 * time.Hour)
	seedTTL(t, f, tid, pid, 310, "delete", expiry, 24*time.Hour)

	broker := events.NewBroker()
	ch, cancel := broker.Subscribe()
	defer cancel()

	mock := &proxmoxtest.MockClient{} // any PVE call panics — warn makes none
	svc := &TTL{Store: f, PVE: mock, Broker: broker, Log: discardLog(), Now: fixedNow()}
	job := ttlJobFor(HandlerTTLWarn, 310, tid, pid, ttlJobPayload{Node: "pve01", GuestType: "qemu", TTLID: "ttl-1", Action: "delete", Which: "24h"})

	if err := svc.TTLWarn(ctx, job); err != nil {
		t.Fatalf("TTLWarn: %v", err)
	}
	// SSE frame published, tenant-scoped, carrying the guest + expiry + action.
	select {
	case e := <-ch:
		if e.Name != "ttl_warning" {
			t.Fatalf("frame name = %q, want ttl_warning", e.Name)
		}
		ev, ok := e.Data.(types.TtlWarningEvent)
		if !ok {
			t.Fatalf("frame data type = %T, want TtlWarningEvent", e.Data)
		}
		if ev.VMID != 310 || ev.Which != "24h" || ev.Action != "delete" || !ev.ExpiresAt.Equal(expiry) {
			t.Fatalf("frame = %+v, want vmid 310 / 24h / delete / %v", ev, expiry)
		}
	default:
		t.Fatal("no ttl_warning frame published")
	}
	ttl, _ := f.GetTTL(ctx, 310)
	if !ttl.Warned24h {
		t.Fatal("warned_24h not set after TTLWarn")
	}

	// Second run with the flag set is a no-op: no PVE call (mock panics), no
	// second frame, and no audit.
	job2 := job
	if err := svc.TTLWarn(ctx, job2); err != nil {
		t.Fatalf("TTLWarn (second): %v", err)
	}
	select {
	case <-ch:
		t.Fatal("second TTLWarn published a frame despite the warned flag")
	default:
	}
	if n := len(f.AllAudit()); n != 0 {
		t.Fatalf("TTLWarn wrote %d audit rows, want 0 (no guest-state change)", n)
	}
}

// --- expire: stop ---

func TestTTLExpireStop(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 320)
	seedTTL(t, f, tid, pid, 320, "stop", fixedNow()().Add(-time.Minute), time.Hour)

	running := true
	var shutdownCalled bool
	mock := &proxmoxtest.MockClient{
		OnGuestStatus: func(_ context.Context, _ pmx.GuestRef) (*pmx.GuestStatusInfo, error) {
			if running {
				return &pmx.GuestStatusInfo{Status: "running", Name: "web-01"}, nil
			}
			return &pmx.GuestStatusInfo{Status: "stopped", Name: "web-01"}, nil
		},
		OnGuestShutdown: func(_ context.Context, _ pmx.GuestRef, _ int) (pmx.UPID, error) {
			shutdownCalled = true
			running = false
			return "UPID:pve01:0:0:0:vzshutdown:320:root@pam:", nil
		},
		// DeleteGuest deliberately nil: a stop-expiry must never destroy.
	}
	svc := &TTL{Store: f, PVE: mock, Log: discardLog(), Now: fixedNow()}
	job := ttlJobFor(HandlerTTLExpire, 320, tid, pid, ttlJobPayload{Node: "pve01", GuestType: "qemu", TTLID: "ttl-1", Action: "stop"})

	if err := svc.TTLExpire(ctx, job); err != nil {
		t.Fatalf("TTLExpire (stop): %v", err)
	}
	if !shutdownCalled {
		t.Fatal("GuestShutdown was not called")
	}
	own, _ := f.GetOwnershipByVMID(ctx, 320)
	if own.ExpiredAt == nil {
		t.Fatal("expired_at not stamped after stop-expiry")
	}
	if own.Status == "tombstoned" {
		t.Fatal("stop-expiry tombstoned the guest (must be reversible)")
	}
	assertSystemAudit(t, f, "guest.ttl.stop", "success")
}

// --- expire: delete ---

func TestTTLExpireDelete(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 330)
	seedTTL(t, f, tid, pid, 330, "delete", fixedNow()().Add(-time.Minute), time.Hour)
	// A live ttl job for the vmid that the destroy choke-point must cancel.
	lingering, _ := f.EnqueueJob(ctx, store.EnqueueJobParams{
		Kind: "one_shot", Handler: HandlerTTLWarn, TenantID: &tid, ProjectID: &pid, VMID: intptr(330),
		RunAt: fixedNow()(),
	})

	var deleteCalled, purge bool
	mock := &proxmoxtest.MockClient{
		OnGuestStatus: func(_ context.Context, _ pmx.GuestRef) (*pmx.GuestStatusInfo, error) {
			return &pmx.GuestStatusInfo{Status: "stopped", Name: "scratch-vm"}, nil
		},
		OnGuestConfig: func(_ context.Context, _ pmx.GuestRef) (map[string]any, error) {
			return map[string]any{"cores": 2, "memory": 2048, "name": "scratch-vm"}, nil
		},
		OnDeleteGuest: func(_ context.Context, _ pmx.GuestRef, p bool) (pmx.UPID, error) {
			deleteCalled = true
			purge = p
			return "UPID:pve01:0:0:0:qmdestroy:330:root@pam:", nil
		},
	}
	svc := &TTL{Store: f, PVE: mock, Log: discardLog(), Now: fixedNow()} // nil Registry → awaitDestroy proceeds
	job := ttlJobFor(HandlerTTLExpire, 330, tid, pid, ttlJobPayload{Node: "pve01", GuestType: "qemu", TTLID: "ttl-1", Action: "delete"})

	if err := svc.TTLExpire(ctx, job); err != nil {
		t.Fatalf("TTLExpire (delete): %v", err)
	}
	if !deleteCalled || !purge {
		t.Fatalf("DeleteGuest called=%v purge=%v, want true/true", deleteCalled, purge)
	}
	own, _ := f.GetOwnershipByVMID(ctx, 330)
	if own.Status != "tombstoned" {
		t.Fatalf("ownership status = %q, want tombstoned after delete-expiry", own.Status)
	}
	if got := f.JobStatus(lingering.ID); got != "cancelled" {
		t.Fatalf("lingering job status = %q, want cancelled", got)
	}
	// Tombstone audit: system actor + full config snapshot in detail.
	var found *store.AuditEntry
	for i, e := range f.AllAudit() {
		if e.Action == "guest.ttl.delete" {
			c := f.AllAudit()[i]
			found = &c
		}
	}
	if found == nil {
		t.Fatal("no guest.ttl.delete audit row")
	}
	if found.ActorSystem == nil || *found.ActorSystem != "system:scheduler" || found.ActorUserID != nil {
		t.Fatalf("delete audit not system-actored: %+v", found)
	}
	if found.Outcome != "success" {
		t.Fatalf("delete audit outcome = %q, want success", found.Outcome)
	}
	var detail map[string]any
	if err := json.Unmarshal(found.Detail, &detail); err != nil {
		t.Fatalf("decode audit detail: %v", err)
	}
	snap, ok := detail["config_snapshot"].(map[string]any)
	if !ok {
		t.Fatalf("audit detail has no config_snapshot object: %v", detail)
	}
	if snap["ttl_id"] == nil || snap["config"] == nil {
		t.Fatalf("config_snapshot missing ttl_id/config: %v", snap)
	}
}

// TestTTLExpireBailsWhenExtended proves the race guard: if a user's Extend landed
// after the expire job was claimed (so its expires_at is now in the future), the
// handler must NOT stop or destroy the guest — the code-review's dangerous race.
func TestTTLExpireBailsWhenExtended(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 331)
	// expires_at in the FUTURE (as if just extended); action delete makes the
	// consequence of NOT bailing maximal.
	seedTTL(t, f, tid, pid, 331, "delete", fixedNow()().Add(time.Hour), time.Hour)
	mock := &proxmoxtest.MockClient{} // any PVE call panics — the guard must make none
	svc := &TTL{Store: f, PVE: mock, Log: discardLog(), Now: fixedNow()}
	job := ttlJobFor(HandlerTTLExpire, 331, tid, pid, ttlJobPayload{Node: "pve01", GuestType: "qemu", TTLID: "ttl-1", Action: "delete"})

	if err := svc.TTLExpire(ctx, job); err != nil {
		t.Fatalf("TTLExpire (extended): %v", err)
	}
	own, _ := f.GetOwnershipByVMID(ctx, 331)
	if own.Status == "tombstoned" {
		t.Fatal("guest was destroyed despite its TTL being extended past now")
	}
	if own.ExpiredAt != nil {
		t.Fatal("guest was marked expired despite its TTL being extended past now")
	}
}

// TestTTLExpireDeleteOwnerGone proves the defensive re-read: a tombstoned owner
// self-cancels its jobs and makes NO PVE call and NO audit.
func TestTTLExpireDeleteOwnerGone(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 331)
	seedTTL(t, f, tid, pid, 331, "delete", fixedNow()().Add(-time.Minute), time.Hour)
	own, _ := f.GetOwnershipByVMID(ctx, 331)
	f.TombstoneOwnership(ctx, own.ID)
	seeded, _ := f.EnqueueJob(ctx, store.EnqueueJobParams{
		Kind: "one_shot", Handler: HandlerTTLExpire, TenantID: &tid, ProjectID: &pid, VMID: intptr(331),
		RunAt: fixedNow()(),
	})

	mock := &proxmoxtest.MockClient{} // any PVE call panics
	svc := &TTL{Store: f, PVE: mock, Log: discardLog(), Now: fixedNow()}
	job := ttlJobFor(HandlerTTLExpire, 331, tid, pid, ttlJobPayload{Node: "pve01", GuestType: "qemu", TTLID: "ttl-1", Action: "delete"})

	if err := svc.TTLExpire(ctx, job); err != nil {
		t.Fatalf("TTLExpire (owner gone): %v", err)
	}
	if got := f.JobStatus(seeded.ID); got != "cancelled" {
		t.Fatalf("owner-gone job status = %q, want cancelled", got)
	}
	if n := len(f.AllAudit()); n != 0 {
		t.Fatalf("owner-gone path wrote %d audit rows, want 0 (no mutation)", n)
	}
}

// --- extend ---

func TestExtendTTLCapsAndResetsFlags(t *testing.T) {
	ctx := context.Background()

	t.Run("uncapped adds one original_duration", func(t *testing.T) {
		f := storetest.New()
		tid, pid := seedGuest(f, 340)
		now := fixedNow()()
		ttl := seedTTL(t, f, tid, pid, 340, "stop", now.Add(2*time.Hour), 24*time.Hour)
		f.SetTTLWarned(ctx, 340, "1h")
		own, _ := f.GetOwnershipByVMID(ctx, 340)
		svc := &TTL{Store: f, Log: discardLog(), Now: fixedNow()}
		got, err := svc.ExtendTTL(ctx, *own, ttl)
		if err != nil {
			t.Fatalf("ExtendTTL: %v", err)
		}
		want := ttl.ExpiresAt.Add(24 * time.Hour)
		if !got.Equal(want) {
			t.Fatalf("new expiry = %v, want %v", got, want)
		}
		after, _ := f.GetTTL(ctx, 340)
		if after.Warned1h || after.Warned24h {
			t.Fatalf("warn flags not reset after extend: %+v", after)
		}
	})

	t.Run("capped at project max_ttl", func(t *testing.T) {
		f := storetest.New()
		tid, pid := seedGuest(f, 341)
		now := fixedNow()()
		// max 3 days; a 30-day extend on a near-expiry TTL is capped to now+3d.
		if _, err := f.UpsertProjectTTLPolicy(ctx, store.UpsertProjectTTLPolicyParams{
			TenantID: tid, ProjectID: pid, MaxTTL: 3 * 24 * time.Hour,
		}); err != nil {
			t.Fatalf("seed policy: %v", err)
		}
		ttl := seedTTL(t, f, tid, pid, 341, "delete", now.Add(time.Hour), 30*24*time.Hour)
		own, _ := f.GetOwnershipByVMID(ctx, 341)
		svc := &TTL{Store: f, Log: discardLog(), Now: fixedNow()}
		got, err := svc.ExtendTTL(ctx, *own, ttl)
		if err != nil {
			t.Fatalf("ExtendTTL: %v", err)
		}
		ceiling := now.Add(3 * 24 * time.Hour)
		if !got.Equal(ceiling) {
			t.Fatalf("capped expiry = %v, want %v", got, ceiling)
		}
	})
}
