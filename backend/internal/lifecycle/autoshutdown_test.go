package lifecycle

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	pmx "github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func fixedNow() func() time.Time {
	return func() time.Time { return time.Date(2026, 1, 5, 12, 0, 0, 0, time.UTC) }
}

// seedGuest seeds a tenant/project/ownership row and returns the ids.
func seedGuest(f *storetest.Fake, vmid int) (tid, pid string) {
	tid = f.AddTenant("Acme", "acme")
	pid = f.AddProject(tid, "Web", "web", "pc-acme-web")
	f.AddOwnership(tid, pid, vmid, "qemu", "pve01", "active", nil)
	return tid, pid
}

func jobFor(handler string, vmid int, tid, pid string, pl jobPayload) store.Job {
	b, _ := json.Marshal(pl)
	return store.Job{
		ID: "job-1", Kind: "recurring", Handler: handler,
		VMID: &vmid, TenantID: &tid, ProjectID: &pid, Payload: b,
		Status: "running",
	}
}

// --- materialization ---

func TestMaterializeForGuestEmitsJobs(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 101)
	start := "07:00"
	if _, err := f.UpsertResourceSchedule(ctx, store.UpsertResourceScheduleParams{
		TenantID: tid, ProjectID: pid, VMID: 101,
		ShutdownTime: "21:45", AutoStartTime: &start, DaysOfWeek: []int{1, 2, 3, 4, 5},
		Timezone: "Europe/Berlin", GraceSeconds: 90, Enabled: true,
	}); err != nil {
		t.Fatalf("seed schedule: %v", err)
	}
	own, _ := f.GetOwnershipByVMID(ctx, 101)
	svc := &AutoShutdown{Store: f, Log: discardLog(), Now: fixedNow()}
	if err := svc.MaterializeForGuest(ctx, *own); err != nil {
		t.Fatalf("MaterializeForGuest: %v", err)
	}

	byHandler := map[string]store.Job{}
	for _, j := range f.AllJobs() {
		byHandler[j.Handler] = j
	}
	if len(byHandler) != 3 {
		t.Fatalf("emitted %d distinct handlers, want 3: %v", len(byHandler), byHandler)
	}
	want := []struct {
		handler, cron, missed string
	}{
		{HandlerStop, "45 21 * * 1,2,3,4,5", "catch_up"},
		{HandlerWarn, "30 21 * * 1,2,3,4,5", "run_late"},
		{HandlerStart, "0 7 * * 1,2,3,4,5", "catch_up"},
	}
	for _, w := range want {
		j, ok := byHandler[w.handler]
		if !ok {
			t.Fatalf("no %s job emitted", w.handler)
		}
		if j.Cron == nil || *j.Cron != w.cron {
			t.Errorf("%s cron = %v, want %q", w.handler, j.Cron, w.cron)
		}
		if j.Timezone == nil || *j.Timezone != "Europe/Berlin" {
			t.Errorf("%s timezone = %v, want Europe/Berlin", w.handler, j.Timezone)
		}
		if j.MissedPolicy != w.missed {
			t.Errorf("%s missed_policy = %q, want %q", w.handler, j.MissedPolicy, w.missed)
		}
		if j.Kind != "recurring" {
			t.Errorf("%s kind = %q, want recurring", w.handler, j.Kind)
		}
		if !j.RunAt.After(svc.Now()) {
			t.Errorf("%s run_at %v not strictly after now", w.handler, j.RunAt)
		}
	}
}

func TestMaterializeNoAutoStartOmitsStartJob(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 102)
	f.UpsertResourceSchedule(ctx, store.UpsertResourceScheduleParams{
		TenantID: tid, ProjectID: pid, VMID: 102,
		ShutdownTime: "22:00", DaysOfWeek: []int{0}, Timezone: "UTC", GraceSeconds: 120, Enabled: true,
	})
	own, _ := f.GetOwnershipByVMID(ctx, 102)
	svc := &AutoShutdown{Store: f, Log: discardLog(), Now: fixedNow()}
	if err := svc.MaterializeForGuest(ctx, *own); err != nil {
		t.Fatalf("MaterializeForGuest: %v", err)
	}
	for _, j := range f.AllJobs() {
		if j.Handler == HandlerStart {
			t.Fatalf("emitted a start job with no auto_start_time set")
		}
	}
}

// TestMaterializeResolution proves resource-over-project precedence and that an
// opt-out (or disabled) resource row emits no jobs (ADR-0019).
func TestMaterializeResolution(t *testing.T) {
	ctx := context.Background()

	t.Run("opt_out emits nothing", func(t *testing.T) {
		f := storetest.New()
		tid, pid := seedGuest(f, 201)
		f.UpsertProjectSchedule(ctx, store.UpsertProjectScheduleParams{
			TenantID: tid, ProjectID: pid, ShutdownTime: "22:00", DaysOfWeek: []int{1}, Timezone: "UTC", GraceSeconds: 120, Enabled: true,
		})
		f.UpsertResourceSchedule(ctx, store.UpsertResourceScheduleParams{
			TenantID: tid, ProjectID: pid, VMID: 201, ShutdownTime: "23:00", DaysOfWeek: []int{1}, Timezone: "UTC", GraceSeconds: 120, Enabled: true, OptOut: true,
		})
		own, _ := f.GetOwnershipByVMID(ctx, 201)
		svc := &AutoShutdown{Store: f, Log: discardLog(), Now: fixedNow()}
		if err := svc.MaterializeForGuest(ctx, *own); err != nil {
			t.Fatalf("MaterializeForGuest: %v", err)
		}
		if n := len(f.AllJobs()); n != 0 {
			t.Fatalf("opt-out emitted %d jobs, want 0", n)
		}
	})

	t.Run("resource wins over project", func(t *testing.T) {
		f := storetest.New()
		tid, pid := seedGuest(f, 202)
		f.UpsertProjectSchedule(ctx, store.UpsertProjectScheduleParams{
			TenantID: tid, ProjectID: pid, ShutdownTime: "22:00", DaysOfWeek: []int{1}, Timezone: "UTC", GraceSeconds: 120, Enabled: true,
		})
		f.UpsertResourceSchedule(ctx, store.UpsertResourceScheduleParams{
			TenantID: tid, ProjectID: pid, VMID: 202, ShutdownTime: "19:30", DaysOfWeek: []int{2}, Timezone: "UTC", GraceSeconds: 60, Enabled: true,
		})
		own, _ := f.GetOwnershipByVMID(ctx, 202)
		svc := &AutoShutdown{Store: f, Log: discardLog(), Now: fixedNow()}
		if err := svc.MaterializeForGuest(ctx, *own); err != nil {
			t.Fatalf("MaterializeForGuest: %v", err)
		}
		var stopCron string
		for _, j := range f.AllJobs() {
			if j.Handler == HandlerStop && j.Cron != nil {
				stopCron = *j.Cron
			}
		}
		if stopCron != "30 19 * * 2" {
			t.Fatalf("stop cron = %q, want resource schedule %q", stopCron, "30 19 * * 2")
		}
	})

	t.Run("project applies when no resource row", func(t *testing.T) {
		f := storetest.New()
		tid, pid := seedGuest(f, 203)
		f.UpsertProjectSchedule(ctx, store.UpsertProjectScheduleParams{
			TenantID: tid, ProjectID: pid, ShutdownTime: "22:00", DaysOfWeek: []int{1}, Timezone: "UTC", GraceSeconds: 120, Enabled: true,
		})
		own, _ := f.GetOwnershipByVMID(ctx, 203)
		svc := &AutoShutdown{Store: f, Log: discardLog(), Now: fixedNow()}
		if err := svc.MaterializeForGuest(ctx, *own); err != nil {
			t.Fatalf("MaterializeForGuest: %v", err)
		}
		var stopCron string
		for _, j := range f.AllJobs() {
			if j.Handler == HandlerStop && j.Cron != nil {
				stopCron = *j.Cron
			}
		}
		if stopCron != "0 22 * * 1" {
			t.Fatalf("stop cron = %q, want project schedule %q", stopCron, "0 22 * * 1")
		}
	})
}

// --- handlers ---

func TestAutoShutdownStopRunning(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 101)

	running := true
	var shutdownCalled bool
	var gotGrace int
	mock := &proxmoxtest.MockClient{
		OnGuestStatus: func(_ context.Context, _ pmx.GuestRef) (*pmx.GuestStatusInfo, error) {
			if running {
				return &pmx.GuestStatusInfo{Status: "running"}, nil
			}
			return &pmx.GuestStatusInfo{Status: "stopped"}, nil
		},
		OnGuestShutdown: func(_ context.Context, _ pmx.GuestRef, grace int) (pmx.UPID, error) {
			shutdownCalled = true
			gotGrace = grace
			running = false
			return "UPID:pve01:0001:0002:0003:vzshutdown:101:root@pam:", nil
		},
	}
	svc := &AutoShutdown{Store: f, PVE: mock, Log: discardLog(), Now: fixedNow()} // nil Registry → await is skipped
	job := jobFor(HandlerStop, 101, tid, pid, jobPayload{ScheduleID: "sched-1", Node: "pve01", GuestType: "qemu", GraceSec: 90})

	if err := svc.AutoShutdownStop(ctx, job); err != nil {
		t.Fatalf("AutoShutdownStop: %v", err)
	}
	if !shutdownCalled {
		t.Fatal("GuestShutdown was not called")
	}
	if gotGrace != 90 {
		t.Errorf("grace = %d, want 90", gotGrace)
	}
	own, _ := f.GetOwnershipByVMID(ctx, 101)
	if !own.AutoStopped {
		t.Error("auto_stopped not set true after scheduler stop")
	}
	assertSystemAudit(t, f, "guest.scheduler.stop", "success")
}

func TestAutoShutdownStopAlreadyStopped(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 101)

	mock := &proxmoxtest.MockClient{
		OnGuestStatus: func(_ context.Context, _ pmx.GuestRef) (*pmx.GuestStatusInfo, error) {
			return &pmx.GuestStatusInfo{Status: "stopped"}, nil
		},
		// OnGuestShutdown deliberately nil: calling it panics the test (must be a no-op).
	}
	svc := &AutoShutdown{Store: f, PVE: mock, Log: discardLog(), Now: fixedNow()}
	job := jobFor(HandlerStop, 101, tid, pid, jobPayload{ScheduleID: "sched-1", Node: "pve01", GuestType: "qemu", GraceSec: 90})

	if err := svc.AutoShutdownStop(ctx, job); err != nil {
		t.Fatalf("AutoShutdownStop (already stopped): %v", err)
	}
	own, _ := f.GetOwnershipByVMID(ctx, 101)
	if own.AutoStopped {
		t.Error("auto_stopped set on an already-stopped guest (should not scheduler-own a user/other stop)")
	}
	// Skipped success is still audited as system.
	assertSystemAudit(t, f, "guest.scheduler.stop", "success")
}

func TestAutoShutdownStopOwnerGone(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 101)
	// Tombstone the guest: the defensive preamble must cancel its jobs and make no
	// PVE call.
	own, _ := f.GetOwnershipByVMID(ctx, 101)
	f.TombstoneOwnership(ctx, own.ID)
	// A live scheduled job for the vmid that must be cancelled.
	seeded, _ := f.EnqueueJob(ctx, store.EnqueueJobParams{
		Kind: "recurring", Handler: HandlerStop, TenantID: &tid, ProjectID: &pid, VMID: intptr(101),
		RunAt: fixedNow()(),
	})

	mock := &proxmoxtest.MockClient{} // any PVE call panics
	svc := &AutoShutdown{Store: f, PVE: mock, Log: discardLog(), Now: fixedNow()}
	job := jobFor(HandlerStop, 101, tid, pid, jobPayload{ScheduleID: "sched-1", Node: "pve01", GuestType: "qemu", GraceSec: 90})

	if err := svc.AutoShutdownStop(ctx, job); err != nil {
		t.Fatalf("AutoShutdownStop (owner gone): %v", err)
	}
	if got := f.JobStatus(seeded.ID); got != "cancelled" {
		t.Fatalf("owner-gone job status = %q, want cancelled", got)
	}
	if n := len(f.AllAudit()); n != 0 {
		t.Fatalf("owner-gone path wrote %d audit rows, want 0 (no mutation)", n)
	}
}

func TestAutoShutdownStartSkipsWhenNotAutoStopped(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 101) // AddOwnership leaves auto_stopped=false

	mock := &proxmoxtest.MockClient{} // GuestStatus/GuestAction nil → panic if called
	svc := &AutoShutdown{Store: f, PVE: mock, Log: discardLog(), Now: fixedNow()}
	job := jobFor(HandlerStart, 101, tid, pid, jobPayload{ScheduleID: "sched-1", Node: "pve01", GuestType: "qemu"})

	if err := svc.AutoShutdownStart(ctx, job); err != nil {
		t.Fatalf("AutoShutdownStart (not auto-stopped): %v", err)
	}
	if n := len(f.AllAudit()); n != 0 {
		t.Fatalf("skip-start wrote %d audit rows, want 0", n)
	}
}

func TestAutoShutdownStartRuns(t *testing.T) {
	ctx := context.Background()
	f := storetest.New()
	tid, pid := seedGuest(f, 101)
	f.SetAutoStopped(ctx, 101, true) // scheduler previously stopped it

	stopped := true
	var startCalled bool
	mock := &proxmoxtest.MockClient{
		OnGuestStatus: func(_ context.Context, _ pmx.GuestRef) (*pmx.GuestStatusInfo, error) {
			if stopped {
				return &pmx.GuestStatusInfo{Status: "stopped"}, nil
			}
			return &pmx.GuestStatusInfo{Status: "running"}, nil
		},
		OnGuestAction: func(_ context.Context, _ pmx.GuestRef, action string) (pmx.UPID, error) {
			if action != "start" {
				t.Errorf("GuestAction action = %q, want start", action)
			}
			startCalled = true
			stopped = false
			return "UPID:pve01:0001:0002:0003:qmstart:101:root@pam:", nil
		},
	}
	svc := &AutoShutdown{Store: f, PVE: mock, Log: discardLog(), Now: fixedNow()}
	job := jobFor(HandlerStart, 101, tid, pid, jobPayload{ScheduleID: "sched-1", Node: "pve01", GuestType: "qemu"})

	if err := svc.AutoShutdownStart(ctx, job); err != nil {
		t.Fatalf("AutoShutdownStart: %v", err)
	}
	if !startCalled {
		t.Fatal("GuestAction(start) was not called")
	}
	own, _ := f.GetOwnershipByVMID(ctx, 101)
	if own.AutoStopped {
		t.Error("auto_stopped not cleared after auto-start")
	}
	assertSystemAudit(t, f, "guest.scheduler.start", "success")
}

func intptr(v int) *int { return &v }

// assertSystemAudit checks exactly one audit row for action exists and is written
// AS SYSTEM (actor_system=system:scheduler, actor_user_id nil) with the outcome.
func assertSystemAudit(t *testing.T, f *storetest.Fake, action, outcome string) {
	t.Helper()
	var found *store.AuditEntry
	for i, e := range f.AllAudit() {
		if e.Action == action {
			c := f.AllAudit()[i]
			found = &c
		}
	}
	if found == nil {
		t.Fatalf("no audit row for action %q", action)
	}
	if found.ActorSystem == nil || *found.ActorSystem != "system:scheduler" {
		t.Errorf("actor_system = %v, want system:scheduler", found.ActorSystem)
	}
	if found.ActorUserID != nil {
		t.Errorf("actor_user_id = %v, want nil (system actor)", *found.ActorUserID)
	}
	if found.Outcome != outcome {
		t.Errorf("outcome = %q, want %q", found.Outcome, outcome)
	}
}
