package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
	_ "time/tzdata" // guarantee IANA zones in the test binary regardless of the host

	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

// counter is a race-safe call counter — the scheduler now dispatches a tick's
// jobs concurrently, so handler-invocation counts are written from goroutines.
type counter struct{ n atomic.Int64 }

func (c *counter) get() int { return int(c.n.Load()) }

// clockAt returns a fake whose clock is pinned to t, plus a pointer to advance it.
func fakeAt(t time.Time) (*storetest.Fake, *time.Time) {
	f := storetest.New()
	now := t
	f.Now = func() time.Time { return now }
	return f, &now
}

// countingHandler returns a HandlerFunc that increments calls and returns err.
func countingHandler(calls *counter, err error) HandlerFunc {
	return func(context.Context, store.Job) error {
		calls.n.Add(1)
		return err
	}
}

func newSched(f *storetest.Fake, clock *time.Time, h map[string]HandlerFunc) *Scheduler {
	return &Scheduler{
		Store:    f,
		Interval: time.Second, // non-zero so Run would tick; tests call Tick directly
		Now:      func() time.Time { return *clock },
		Handlers: h,
	}
}

func TestTickOneShotSuccess(t *testing.T) {
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	f, clock := fakeAt(base)
	var calls counter
	s := newSched(f, clock, map[string]HandlerFunc{"noop": countingHandler(&calls, nil)})

	vmid := 101
	job, err := f.EnqueueJob(context.Background(), store.EnqueueJobParams{
		Kind: "one_shot", Handler: "noop", VMID: &vmid, RunAt: base,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	if n := s.Tick(context.Background()); n != 1 {
		t.Fatalf("Tick processed %d jobs, want 1", n)
	}
	if calls.get() != 1 {
		t.Fatalf("handler called %d times, want 1", calls.get())
	}
	if got := f.JobStatus(job.ID); got != "succeeded" {
		t.Fatalf("job status = %q, want succeeded", got)
	}

	// Idempotency at the store level: a second tick finds nothing due (terminal).
	if n := s.Tick(context.Background()); n != 0 {
		t.Fatalf("second Tick processed %d jobs, want 0", n)
	}
	if calls.get() != 1 {
		t.Fatalf("handler re-fired a terminal job (calls=%d)", calls.get())
	}
}

func TestTickNotDueIsSkipped(t *testing.T) {
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	f, clock := fakeAt(base)
	var calls counter
	s := newSched(f, clock, map[string]HandlerFunc{"noop": countingHandler(&calls, nil)})

	vmid := 1
	if _, err := f.EnqueueJob(context.Background(), store.EnqueueJobParams{
		Kind: "one_shot", Handler: "noop", VMID: &vmid, RunAt: base.Add(time.Hour),
	}); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if n := s.Tick(context.Background()); n != 0 {
		t.Fatalf("Tick processed %d future jobs, want 0", n)
	}
	if calls.get() != 0 {
		t.Fatalf("future job fired early (calls=%d)", calls.get())
	}
}

func TestTickHandlerErrorRetriesThenDeadLetters(t *testing.T) {
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	f, clock := fakeAt(base)
	var calls counter
	s := newSched(f, clock, map[string]HandlerFunc{"boom": countingHandler(&calls, errors.New("kaboom"))})

	vmid := 7
	job, err := f.EnqueueJob(context.Background(), store.EnqueueJobParams{
		Kind: "one_shot", Handler: "boom", VMID: &vmid, RunAt: base, MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// First tick: handler errors → retry (still scheduled, attempts=1).
	s.Tick(context.Background())
	if got := f.JobStatus(job.ID); got != "scheduled" {
		t.Fatalf("after 1st failure status = %q, want scheduled (retry)", got)
	}

	// The retry is scheduled with backoff into the future; advance the clock past
	// it so the second tick claims it.
	*clock = base.Add(10 * time.Minute)
	s.Tick(context.Background())
	if calls.get() != 2 {
		t.Fatalf("handler called %d times, want 2", calls.get())
	}
	if got := f.JobStatus(job.ID); got != "failed" {
		t.Fatalf("after max attempts status = %q, want failed (dead-letter)", got)
	}

	// Dead-lettered jobs stop retrying.
	*clock = base.Add(time.Hour)
	if n := s.Tick(context.Background()); n != 0 {
		t.Fatalf("dead-lettered job re-claimed (%d)", n)
	}
	if calls.get() != 2 {
		t.Fatalf("dead-lettered job re-fired (calls=%d)", calls.get())
	}
}

func TestTickRecurringReschedules(t *testing.T) {
	// 12:00 UTC; a daily 03:00 UTC job's next fire is 03:00 the following day.
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	f, clock := fakeAt(base)
	var calls counter
	s := newSched(f, clock, map[string]HandlerFunc{"tick": countingHandler(&calls, nil)})

	cron := "0 3 * * *"
	tz := "UTC"
	vmid := 42
	job, err := f.EnqueueJob(context.Background(), store.EnqueueJobParams{
		Kind: "recurring", Handler: "tick", VMID: &vmid, RunAt: base, Cron: &cron, Timezone: &tz,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	s.Tick(context.Background())
	if calls.get() != 1 {
		t.Fatalf("recurring handler called %d times, want 1", calls.get())
	}
	got, err := f.GetJob(context.Background(), job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != "scheduled" {
		t.Fatalf("recurring status = %q, want scheduled", got.Status)
	}
	want := time.Date(2026, 8, 28, 3, 0, 0, 0, time.UTC)
	if !got.RunAt.Equal(want) {
		t.Fatalf("next run_at = %s, want %s", got.RunAt.UTC(), want)
	}
	if got.Attempts != 0 {
		t.Fatalf("reschedule left attempts = %d, want 0", got.Attempts)
	}
}

func TestTickUnknownHandlerDeadLetters(t *testing.T) {
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	f, clock := fakeAt(base)
	s := newSched(f, clock, map[string]HandlerFunc{}) // no handlers registered

	vmid := 5
	job, err := f.EnqueueJob(context.Background(), store.EnqueueJobParams{
		Kind: "one_shot", Handler: "missing", VMID: &vmid, RunAt: base, MaxAttempts: 1,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	s.Tick(context.Background())
	if got := f.JobStatus(job.ID); got != "failed" {
		t.Fatalf("unknown-handler job status = %q, want failed", got)
	}
}

func TestMissedPolicy(t *testing.T) {
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	// run_at is 10 minutes in the past → genuinely missed (default threshold 2m).
	pastRunAt := base.Add(-10 * time.Minute)

	t.Run("skip abandons the occurrence", func(t *testing.T) {
		f, clock := fakeAt(base)
		var calls counter
		s := newSched(f, clock, map[string]HandlerFunc{"noop": countingHandler(&calls, nil)})
		vmid := 1
		job, _ := f.EnqueueJob(context.Background(), store.EnqueueJobParams{
			Kind: "one_shot", Handler: "noop", VMID: &vmid, RunAt: pastRunAt, MissedPolicy: "skip",
		})
		s.Tick(context.Background())
		if calls.get() != 0 {
			t.Fatalf("skip: handler ran (calls=%d), want 0", calls.get())
		}
		if got := f.JobStatus(job.ID); got != "succeeded" {
			t.Fatalf("skip: status = %q, want succeeded (settled without running)", got)
		}
	})

	t.Run("run_late still executes", func(t *testing.T) {
		f, clock := fakeAt(base)
		var calls counter
		s := newSched(f, clock, map[string]HandlerFunc{"noop": countingHandler(&calls, nil)})
		vmid := 2
		if _, err := f.EnqueueJob(context.Background(), store.EnqueueJobParams{
			Kind: "one_shot", Handler: "noop", VMID: &vmid, RunAt: pastRunAt, MissedPolicy: "run_late",
		}); err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
		s.Tick(context.Background())
		if calls.get() != 1 {
			t.Fatalf("run_late: handler called %d times, want 1", calls.get())
		}
	})
}

func TestTickReclaimsStaleRunning(t *testing.T) {
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	f, clock := fakeAt(base)
	var calls counter
	s := newSched(f, clock, map[string]HandlerFunc{"noop": countingHandler(&calls, nil)})

	vmid := 9
	job, err := f.EnqueueJob(context.Background(), store.EnqueueJobParams{
		Kind: "one_shot", Handler: "noop", VMID: &vmid, RunAt: base,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	// Simulate a crash mid-handler: claim the job (→ running, locked_at=base) but
	// never settle it.
	if _, err := f.ClaimDueJobs(context.Background(), base, 10, "dead-instance"); err != nil {
		t.Fatalf("ClaimDueJobs: %v", err)
	}
	if got := f.JobStatus(job.ID); got != "running" {
		t.Fatalf("precondition: status = %q, want running", got)
	}

	// Advance past the stale-after window; the next tick reclaims and processes it.
	*clock = base.Add(defaultStaleAfter + time.Minute)
	s.Tick(context.Background())
	if calls.get() != 1 {
		t.Fatalf("stale job not reclaimed+run (calls=%d, want 1)", calls.get())
	}
	if got := f.JobStatus(job.ID); got != "succeeded" {
		t.Fatalf("reclaimed job status = %q, want succeeded", got)
	}
}

func TestTickCancelledJobNeverFires(t *testing.T) {
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	f, clock := fakeAt(base)
	var calls counter
	s := newSched(f, clock, map[string]HandlerFunc{"noop": countingHandler(&calls, nil)})

	vmid := 3
	if _, err := f.EnqueueJob(context.Background(), store.EnqueueJobParams{
		Kind: "one_shot", Handler: "noop", VMID: &vmid, RunAt: base,
	}); err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	// Guest destroyed → its jobs cancelled at the choke-point.
	if n, err := f.CancelJobsForVMID(context.Background(), vmid); err != nil || n != 1 {
		t.Fatalf("CancelJobsForVMID = (%d, %v), want (1, nil)", n, err)
	}
	if n := s.Tick(context.Background()); n != 0 {
		t.Fatalf("cancelled job was claimed (%d)", n)
	}
	if calls.get() != 0 {
		t.Fatalf("cancelled job fired (calls=%d)", calls.get())
	}
}

// TestCancelWhileRunningIsNotResurrected guards the race the code review flagged:
// a job cancelled while its handler is in flight (owning guest destroyed) must
// not be flipped back to succeeded/scheduled by the settle path.
func TestCancelWhileRunningIsNotResurrected(t *testing.T) {
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	ctx := context.Background()

	t.Run("CompleteJob no-ops on a cancelled one-shot", func(t *testing.T) {
		f, _ := fakeAt(base)
		vmid := 55
		job, _ := f.EnqueueJob(ctx, store.EnqueueJobParams{Kind: "one_shot", Handler: "noop", VMID: &vmid, RunAt: base})
		if _, err := f.ClaimDueJobs(ctx, base, 10, "inst"); err != nil { // -> running
			t.Fatalf("ClaimDueJobs: %v", err)
		}
		if _, err := f.CancelJobsForVMID(ctx, vmid); err != nil { // -> cancelled mid-run
			t.Fatalf("CancelJobsForVMID: %v", err)
		}
		if err := f.CompleteJob(ctx, job.ID); err != nil {
			t.Fatalf("CompleteJob: %v", err)
		}
		if got := f.JobStatus(job.ID); got != "cancelled" {
			t.Fatalf("CompleteJob resurrected a cancelled job to %q", got)
		}
	})

	t.Run("RescheduleRecurring no-ops on a cancelled recurring", func(t *testing.T) {
		f, _ := fakeAt(base)
		cron, tz := "0 3 * * *", "UTC"
		vmid := 56
		job, _ := f.EnqueueJob(ctx, store.EnqueueJobParams{Kind: "recurring", Handler: "tick", VMID: &vmid, RunAt: base, Cron: &cron, Timezone: &tz})
		if _, err := f.ClaimDueJobs(ctx, base, 10, "inst"); err != nil {
			t.Fatalf("ClaimDueJobs: %v", err)
		}
		if _, err := f.CancelJobsForVMID(ctx, vmid); err != nil {
			t.Fatalf("CancelJobsForVMID: %v", err)
		}
		if err := f.RescheduleRecurring(ctx, job.ID, base.Add(24*time.Hour)); err != nil {
			t.Fatalf("RescheduleRecurring: %v", err)
		}
		if got := f.JobStatus(job.ID); got != "cancelled" {
			t.Fatalf("RescheduleRecurring re-armed a cancelled job to %q", got)
		}
	})

	t.Run("FailJob no-ops on a cancelled job", func(t *testing.T) {
		f, _ := fakeAt(base)
		vmid := 57
		job, _ := f.EnqueueJob(ctx, store.EnqueueJobParams{Kind: "one_shot", Handler: "boom", VMID: &vmid, RunAt: base})
		if _, err := f.ClaimDueJobs(ctx, base, 10, "inst"); err != nil {
			t.Fatalf("ClaimDueJobs: %v", err)
		}
		if _, err := f.CancelJobsForVMID(ctx, vmid); err != nil {
			t.Fatalf("CancelJobsForVMID: %v", err)
		}
		dead, err := f.FailJob(ctx, job.ID, "boom", base.Add(time.Minute))
		if err != nil || dead {
			t.Fatalf("FailJob on cancelled = (%v, %v), want (false, nil)", dead, err)
		}
		if got := f.JobStatus(job.ID); got != "cancelled" {
			t.Fatalf("FailJob changed a cancelled job to %q", got)
		}
	})
}

// TestHandlerPanicDoesNotCrash proves a panicking handler is converted to a
// normal failure (retry→dead-letter) instead of taking down the process.
func TestHandlerPanicDoesNotCrash(t *testing.T) {
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	f, clock := fakeAt(base)
	s := newSched(f, clock, map[string]HandlerFunc{
		"panic": func(context.Context, store.Job) error { panic("boom in handler") },
	})
	vmid := 88
	job, _ := f.EnqueueJob(context.Background(), store.EnqueueJobParams{
		Kind: "one_shot", Handler: "panic", VMID: &vmid, RunAt: base, MaxAttempts: 1,
	})
	// Must not panic out of Tick.
	s.Tick(context.Background())
	if got := f.JobStatus(job.ID); got != "failed" {
		t.Fatalf("panicking handler job status = %q, want failed (dead-lettered)", got)
	}
}

func TestClaimLimitBounded(t *testing.T) {
	base := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	f, clock := fakeAt(base)
	var calls counter
	s := newSched(f, clock, map[string]HandlerFunc{"noop": countingHandler(&calls, nil)})
	s.ClaimLimit = 2

	for i := 0; i < 5; i++ {
		vmid := 100 + i
		if _, err := f.EnqueueJob(context.Background(), store.EnqueueJobParams{
			Kind: "one_shot", Handler: "noop", VMID: &vmid, RunAt: base,
		}); err != nil {
			t.Fatalf("EnqueueJob: %v", err)
		}
	}
	if n := s.Tick(context.Background()); n != 2 {
		t.Fatalf("Tick processed %d, want ClaimLimit=2", n)
	}
}

func TestNextCron(t *testing.T) {
	tests := []struct {
		name string
		expr string
		tz   string
		from time.Time
		want time.Time
	}{
		{
			name: "daily noon UTC",
			expr: "0 12 * * *",
			tz:   "UTC",
			from: time.Date(2026, 8, 27, 15, 0, 0, 0, time.UTC),
			want: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
		},
		{
			name: "empty tz defaults to UTC",
			expr: "30 6 * * *",
			tz:   "",
			from: time.Date(2026, 8, 27, 6, 0, 0, 0, time.UTC),
			want: time.Date(2026, 8, 27, 6, 30, 0, 0, time.UTC),
		},
		{
			name: "berlin 19:00 during CEST (UTC+2)",
			expr: "0 19 * * *",
			tz:   "Europe/Berlin",
			from: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
			// 19:00 CEST == 17:00 UTC.
			want: time.Date(2026, 8, 27, 17, 0, 0, 0, time.UTC),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NextCron(tc.expr, tc.tz, tc.from)
			if err != nil {
				t.Fatalf("NextCron: %v", err)
			}
			if !got.Equal(tc.want) {
				t.Fatalf("NextCron = %s, want %s", got.UTC(), tc.want)
			}
		})
	}

	t.Run("invalid cron errors", func(t *testing.T) {
		if _, err := NextCron("not a cron", "UTC", time.Now()); err == nil {
			t.Fatal("expected error for invalid cron expression")
		}
	})
	t.Run("invalid timezone errors", func(t *testing.T) {
		if _, err := NextCron("0 12 * * *", "Mars/Phobos", time.Now()); err == nil {
			t.Fatal("expected error for invalid timezone")
		}
	})
}
