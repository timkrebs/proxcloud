package store

import (
	"context"
	"sync"
	"testing"
	"time"
)

// resetJobsTables clears the scheduler job store between integration tests.
// Guarded against non-ephemeral databases (see guardDestructive).
func resetJobsTables(t *testing.T, s *PgStore) {
	t.Helper()
	guardDestructive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.pool.Exec(ctx, `DELETE FROM jobs`); err != nil {
		t.Fatalf("reset jobs: %v", err)
	}
}

func vptr(v int) *int { return &v }

// defaultOwner returns the migration-seeded default tenant + project ids as the
// pointers EnqueueJobParams wants. Resource jobs must set tenant+project+vmid
// together (the jobs_owner_all_or_none CHECK), so job tests own a real project.
func defaultOwner(t *testing.T, s *PgStore) (*string, *string) {
	t.Helper()
	ctx := context.Background()
	ten, err := s.GetTenantBySlug(ctx, "default")
	if err != nil {
		t.Fatalf("default tenant: %v", err)
	}
	proj, err := s.GetProjectByPoolID(ctx, "pc-default-default")
	if err != nil {
		t.Fatalf("default project: %v", err)
	}
	return &ten.ID, &proj.ID
}

func TestJobLifecycleEnqueueClaimComplete(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetJobsTables(t, s)
	t.Cleanup(func() { resetJobsTables(t, s) })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	tid, pid := defaultOwner(t, s)

	job, err := s.EnqueueJob(ctx, EnqueueJobParams{TenantID: tid, ProjectID: pid,
		Kind: "one_shot", Handler: "ttl.expire", VMID: vptr(101),
		Payload: []byte(`{"reason":"test"}`), RunAt: now,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}
	if job.Status != "scheduled" || job.Attempts != 0 || job.MaxAttempts != 5 || job.MissedPolicy != "catch_up" {
		t.Fatalf("enqueued job defaults wrong: %+v", job)
	}

	// Not due yet: a claim strictly before run_at returns nothing.
	if claimed, err := s.ClaimDueJobs(ctx, now.Add(-time.Second), 10, "inst"); err != nil || len(claimed) != 0 {
		t.Fatalf("ClaimDueJobs(before) = (%d, %v), want (0, nil)", len(claimed), err)
	}

	claimed, err := s.ClaimDueJobs(ctx, now, 10, "inst-a")
	if err != nil {
		t.Fatalf("ClaimDueJobs: %v", err)
	}
	if len(claimed) != 1 {
		t.Fatalf("claimed %d jobs, want 1", len(claimed))
	}
	c := claimed[0]
	if c.Status != "running" || c.LockedBy == nil || *c.LockedBy != "inst-a" || c.LockedAt == nil {
		t.Fatalf("claimed job not properly locked: %+v", c)
	}

	// A second claim finds nothing (already running).
	if again, err := s.ClaimDueJobs(ctx, now, 10, "inst-b"); err != nil || len(again) != 0 {
		t.Fatalf("ClaimDueJobs(again) = (%d, %v), want (0, nil)", len(again), err)
	}

	if err := s.CompleteJob(ctx, job.ID); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}
	got, err := s.GetJob(ctx, job.ID)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != "succeeded" || got.LockedAt != nil {
		t.Fatalf("completed job = %+v, want succeeded + cleared lock", got)
	}
}

// TestJobClaimSkipLockedNoDoubleFire is the core concurrency guarantee (ADR-0018):
// many instances ticking at once must partition the due jobs — every job claimed
// exactly once, never twice.
func TestJobClaimSkipLockedNoDoubleFire(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetJobsTables(t, s)
	t.Cleanup(func() { resetJobsTables(t, s) })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	tid, pid := defaultOwner(t, s)

	const numJobs = 60
	for i := 0; i < numJobs; i++ {
		if _, err := s.EnqueueJob(ctx, EnqueueJobParams{TenantID: tid, ProjectID: pid,
			Kind: "one_shot", Handler: "autoshutdown.stop", VMID: vptr(1000 + i), RunAt: now,
		}); err != nil {
			t.Fatalf("EnqueueJob %d: %v", i, err)
		}
	}

	const workers = 8
	var wg sync.WaitGroup
	start := make(chan struct{})
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start // release all workers together to maximise contention
			for {
				claimed, err := s.ClaimDueJobs(ctx, now, 5, "inst")
				if err != nil {
					t.Errorf("worker %d ClaimDueJobs: %v", idx, err)
					return
				}
				if len(claimed) == 0 {
					return
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()

	// SKIP LOCKED partitions the rows: after the storm every job must be claimed
	// exactly once, i.e. all are now 'running' and none left 'scheduled'.
	total := 0
	rows, err := s.pool.Query(ctx, `SELECT id::text, status FROM jobs`)
	if err != nil {
		t.Fatalf("query jobs: %v", err)
	}
	defer rows.Close()
	running := 0
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if status == "running" {
			running++
		}
		total++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if total != numJobs {
		t.Fatalf("job count = %d, want %d", total, numJobs)
	}
	if running != numJobs {
		t.Fatalf("running (claimed exactly once) = %d, want %d", running, numJobs)
	}
}

func TestJobFailRetryThenDeadLetter(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetJobsTables(t, s)
	t.Cleanup(func() { resetJobsTables(t, s) })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	tid, pid := defaultOwner(t, s)

	job, err := s.EnqueueJob(ctx, EnqueueJobParams{TenantID: tid, ProjectID: pid,
		Kind: "one_shot", Handler: "boom", VMID: vptr(202), RunAt: now, MaxAttempts: 2,
	})
	if err != nil {
		t.Fatalf("EnqueueJob: %v", err)
	}

	// FailJob only acts on a claimed (running) job — mirror the real flow: claim,
	// fail (retry), claim again at the retry time, fail again (dead-letter).
	retryAt := now.Add(30 * time.Second)
	if _, err := s.ClaimDueJobs(ctx, now, 10, "inst"); err != nil {
		t.Fatalf("ClaimDueJobs 1: %v", err)
	}
	dead, err := s.FailJob(ctx, job.ID, "first failure", retryAt)
	if err != nil {
		t.Fatalf("FailJob 1: %v", err)
	}
	if dead {
		t.Fatal("first failure dead-lettered too early")
	}
	got, _ := s.GetJob(ctx, job.ID)
	if got.Status != "scheduled" || got.Attempts != 1 || got.LastError == nil || !got.RunAt.Equal(retryAt) {
		t.Fatalf("after 1st failure = %+v, want scheduled/attempts=1/retryAt", got)
	}

	if _, err := s.ClaimDueJobs(ctx, retryAt, 10, "inst"); err != nil {
		t.Fatalf("ClaimDueJobs 2: %v", err)
	}
	dead, err = s.FailJob(ctx, job.ID, "second failure", retryAt.Add(time.Minute))
	if err != nil {
		t.Fatalf("FailJob 2: %v", err)
	}
	if !dead {
		t.Fatal("second failure should dead-letter at max_attempts=2")
	}
	got, _ = s.GetJob(ctx, job.ID)
	if got.Status != "failed" || got.Attempts != 2 {
		t.Fatalf("after 2nd failure = %+v, want failed/attempts=2", got)
	}
}

func TestJobCancelForVMID(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetJobsTables(t, s)
	t.Cleanup(func() { resetJobsTables(t, s) })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	tid, pid := defaultOwner(t, s)

	// Two jobs for vmid 303 (one scheduled, one running), one for 304.
	j1, _ := s.EnqueueJob(ctx, EnqueueJobParams{TenantID: tid, ProjectID: pid, Kind: "one_shot", Handler: "ttl.warn", VMID: vptr(303), RunAt: now})
	j2, _ := s.EnqueueJob(ctx, EnqueueJobParams{TenantID: tid, ProjectID: pid, Kind: "one_shot", Handler: "ttl.expire", VMID: vptr(303), RunAt: now.Add(time.Hour)})
	other, _ := s.EnqueueJob(ctx, EnqueueJobParams{TenantID: tid, ProjectID: pid, Kind: "one_shot", Handler: "ttl.expire", VMID: vptr(304), RunAt: now})
	if _, err := s.ClaimDueJobs(ctx, now, 10, "inst"); err != nil { // moves j1 → running
		t.Fatalf("ClaimDueJobs: %v", err)
	}

	n, err := s.CancelJobsForVMID(ctx, 303)
	if err != nil {
		t.Fatalf("CancelJobsForVMID: %v", err)
	}
	if n != 2 {
		t.Fatalf("cancelled %d jobs for vmid 303, want 2", n)
	}
	for _, id := range []string{j1.ID, j2.ID} {
		if got, _ := s.GetJob(ctx, id); got.Status != "cancelled" {
			t.Fatalf("job %s status = %q, want cancelled", id, got.Status)
		}
	}
	// vmid 304 untouched.
	if got, _ := s.GetJob(ctx, other.ID); got.Status == "cancelled" {
		t.Fatal("CancelJobsForVMID(303) cancelled a vmid-304 job")
	}
	// Idempotent: a second cancel finds nothing.
	if n, _ := s.CancelJobsForVMID(ctx, 303); n != 0 {
		t.Fatalf("second CancelJobsForVMID = %d, want 0", n)
	}
}

// TestJobSettleGuardedByRunningStatus proves the code-review race fix: a job
// cancelled while its handler is in flight must not be resurrected by the settle
// path (Complete/Reschedule/Fail all guard on status='running').
func TestJobSettleGuardedByRunningStatus(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetJobsTables(t, s)
	t.Cleanup(func() { resetJobsTables(t, s) })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	tid, pid := defaultOwner(t, s)

	// one-shot: claim -> cancel -> Complete must be a no-op.
	j1, _ := s.EnqueueJob(ctx, EnqueueJobParams{TenantID: tid, ProjectID: pid, Kind: "one_shot", Handler: "noop", VMID: vptr(701), RunAt: now})
	if _, err := s.ClaimDueJobs(ctx, now, 10, "inst"); err != nil {
		t.Fatalf("ClaimDueJobs: %v", err)
	}
	if _, err := s.CancelJobsForVMID(ctx, 701); err != nil {
		t.Fatalf("CancelJobsForVMID: %v", err)
	}
	if err := s.CompleteJob(ctx, j1.ID); err != nil {
		t.Fatalf("CompleteJob: %v", err)
	}
	if got, _ := s.GetJob(ctx, j1.ID); got.Status != "cancelled" {
		t.Fatalf("CompleteJob resurrected a cancelled job to %q", got.Status)
	}

	// recurring: claim -> cancel -> Reschedule must be a no-op.
	cron, tz := "0 3 * * *", "UTC"
	j2, _ := s.EnqueueJob(ctx, EnqueueJobParams{TenantID: tid, ProjectID: pid, Kind: "recurring", Handler: "tick", VMID: vptr(702), RunAt: now, Cron: &cron, Timezone: &tz})
	if _, err := s.ClaimDueJobs(ctx, now, 10, "inst"); err != nil {
		t.Fatalf("ClaimDueJobs: %v", err)
	}
	if _, err := s.CancelJobsForVMID(ctx, 702); err != nil {
		t.Fatalf("CancelJobsForVMID: %v", err)
	}
	if err := s.RescheduleRecurring(ctx, j2.ID, now.Add(24*time.Hour)); err != nil {
		t.Fatalf("RescheduleRecurring: %v", err)
	}
	if got, _ := s.GetJob(ctx, j2.ID); got.Status != "cancelled" {
		t.Fatalf("RescheduleRecurring re-armed a cancelled job to %q", got.Status)
	}

	// FailJob on a cancelled job is a no-op returning (false, nil).
	j3, _ := s.EnqueueJob(ctx, EnqueueJobParams{TenantID: tid, ProjectID: pid, Kind: "one_shot", Handler: "boom", VMID: vptr(703), RunAt: now})
	if _, err := s.ClaimDueJobs(ctx, now, 10, "inst"); err != nil {
		t.Fatalf("ClaimDueJobs: %v", err)
	}
	if _, err := s.CancelJobsForVMID(ctx, 703); err != nil {
		t.Fatalf("CancelJobsForVMID: %v", err)
	}
	dead, err := s.FailJob(ctx, j3.ID, "boom", now.Add(time.Minute))
	if err != nil || dead {
		t.Fatalf("FailJob on cancelled = (%v, %v), want (false, nil)", dead, err)
	}
	if got, _ := s.GetJob(ctx, j3.ID); got.Status != "cancelled" {
		t.Fatalf("FailJob changed a cancelled job to %q", got.Status)
	}
}

func TestJobReclaimStaleRunning(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetJobsTables(t, s)
	t.Cleanup(func() { resetJobsTables(t, s) })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	tid, pid := defaultOwner(t, s)

	job, _ := s.EnqueueJob(ctx, EnqueueJobParams{TenantID: tid, ProjectID: pid, Kind: "one_shot", Handler: "noop", VMID: vptr(404), RunAt: now.Add(-time.Hour)})
	if _, err := s.ClaimDueJobs(ctx, now, 10, "dead-instance"); err != nil {
		t.Fatalf("ClaimDueJobs: %v", err)
	}
	if got, _ := s.GetJob(ctx, job.ID); got.Status != "running" {
		t.Fatalf("precondition status = %q, want running", got.Status)
	}

	// Nothing is stale relative to a cutoff before the claim.
	if n, err := s.ReclaimStaleRunning(ctx, now.Add(-time.Minute)); err != nil || n != 0 {
		t.Fatalf("ReclaimStaleRunning(fresh) = (%d, %v), want (0, nil)", n, err)
	}
	// A cutoff after the claim reclaims it.
	n, err := s.ReclaimStaleRunning(ctx, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ReclaimStaleRunning: %v", err)
	}
	if n != 1 {
		t.Fatalf("reclaimed %d, want 1", n)
	}
	got, _ := s.GetJob(ctx, job.ID)
	if got.Status != "scheduled" || got.LockedAt != nil || got.LockedBy != nil {
		t.Fatalf("reclaimed job = %+v, want scheduled + cleared lock", got)
	}
}

func TestListJobsFilters(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetJobsTables(t, s)
	t.Cleanup(func() { resetJobsTables(t, s) })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	tid, pid := defaultOwner(t, s)

	s.EnqueueJob(ctx, EnqueueJobParams{TenantID: tid, ProjectID: pid, Kind: "one_shot", Handler: "a", VMID: vptr(501), RunAt: now})
	s.EnqueueJob(ctx, EnqueueJobParams{TenantID: tid, ProjectID: pid, Kind: "one_shot", Handler: "b", VMID: vptr(502), RunAt: now.Add(time.Hour)})

	all, err := s.ListJobs(ctx, JobFilter{})
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("ListJobs(all) = %d, want 2", len(all))
	}
	// Newest run_at first.
	if all[0].VMID == nil || *all[0].VMID != 502 {
		t.Fatalf("ListJobs order wrong: first vmid = %v, want 502", all[0].VMID)
	}
	// VMID filter.
	one, err := s.ListJobs(ctx, JobFilter{VMID: vptr(501)})
	if err != nil {
		t.Fatalf("ListJobs(vmid): %v", err)
	}
	if len(one) != 1 || *one[0].VMID != 501 {
		t.Fatalf("ListJobs(vmid=501) = %+v, want single 501", one)
	}
	// Status filter.
	sched, _ := s.ListJobs(ctx, JobFilter{Status: "scheduled"})
	if len(sched) != 2 {
		t.Fatalf("ListJobs(scheduled) = %d, want 2", len(sched))
	}
}
