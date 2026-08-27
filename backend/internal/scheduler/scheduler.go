// Package scheduler runs Proxcloud's persistent, tenant-aware job engine
// (ADR-0018). It is the reconciler's sibling: the same Run/Tick ticker shape and
// injectable Now clock, but instead of a single fixed sweep it claims due jobs
// from the Postgres `jobs` table with SELECT … FOR UPDATE SKIP LOCKED (so a
// second backend instance never double-fires), dispatches each to a registered
// handler, and applies retry→backoff→dead-letter and the per-job missed-window
// policy. Handlers are idempotent and defensive (they re-read ownership and
// self-cancel when their guest is gone), giving at-least-once delivery.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// Default tuning. StaleAfter MUST exceed HandlerTimeout so a still-running
// handler is never reclaimed out from under itself; HandlerTimeout in turn must
// exceed the longest auto-shutdown/TTL grace window (default 120s) with margin.
const (
	defaultClaimLimit     = 20
	defaultHandlerTimeout = 10 * time.Minute
	defaultStaleAfter     = 15 * time.Minute
	defaultBaseBackoff    = 30 * time.Second
	defaultMaxBackoff     = time.Hour
	// A job whose run_at is more than this far in the past was genuinely missed
	// (the worker was down/overloaded), not merely due — this gates missed_policy.
	defaultMissedThreshold = 2 * time.Minute
)

// HandlerFunc executes one job. It MUST be idempotent (at-least-once delivery
// can run it twice) and defensive (re-read ownership; if the owning guest is
// gone, cancel the owner's jobs via Store.CancelJobsForVMID and return nil). A
// returned error triggers retry→backoff→dead-letter.
type HandlerFunc func(ctx context.Context, job store.Job) error

// Scheduler claims and dispatches jobs. Now is injectable for deterministic
// tests (defaults to time.Now); Handlers maps a job's Handler key to its func.
type Scheduler struct {
	Store    store.Store
	Log      *slog.Logger
	Interval time.Duration
	Now      func() time.Time

	// InstanceID stamps locked_by so a claim is attributable to this backend.
	InstanceID string
	// Handlers dispatches by job.Handler; register with Register before Run.
	Handlers map[string]HandlerFunc

	// Overridable tuning (zero → the defaults above).
	ClaimLimit      int
	HandlerTimeout  time.Duration
	StaleAfter      time.Duration
	BaseBackoff     time.Duration
	MaxBackoff      time.Duration
	MissedThreshold time.Duration
}

// Register wires a handler func to a handler key (e.g. "autoshutdown.stop").
func (s *Scheduler) Register(handler string, fn HandlerFunc) {
	if s.Handlers == nil {
		s.Handlers = map[string]HandlerFunc{}
	}
	s.Handlers[handler] = fn
}

func (s *Scheduler) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) instanceID() string {
	if s.InstanceID != "" {
		return s.InstanceID
	}
	return "scheduler"
}

func (s *Scheduler) claimLimit() int {
	if s.ClaimLimit > 0 {
		return s.ClaimLimit
	}
	return defaultClaimLimit
}

func (s *Scheduler) handlerTimeout() time.Duration {
	if s.HandlerTimeout > 0 {
		return s.HandlerTimeout
	}
	return defaultHandlerTimeout
}

func (s *Scheduler) staleAfter() time.Duration {
	if s.StaleAfter > 0 {
		return s.StaleAfter
	}
	return defaultStaleAfter
}

func (s *Scheduler) missedThreshold() time.Duration {
	if s.MissedThreshold > 0 {
		return s.MissedThreshold
	}
	return defaultMissedThreshold
}

// Run ticks immediately, then every Interval until ctx is cancelled (graceful
// shutdown shares the server's background context). A non-positive Interval
// disables the loop (logged) so a misconfiguration fails visible, not silent.
func (s *Scheduler) Run(ctx context.Context) {
	if s.Interval <= 0 {
		s.logger().Warn("scheduler disabled (non-positive interval)")
		return
	}
	s.logger().Info("scheduler started",
		"interval", s.Interval.String(), "instance", s.instanceID(),
		"handlers", len(s.Handlers), "stale_after", s.staleAfter().String())
	s.Tick(ctx)
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.logger().Info("scheduler stopped")
			return
		case <-ticker.C:
			s.Tick(ctx)
		}
	}
}

// Tick performs one scheduler pass: reclaim stale-running jobs (a crashed
// backend's in-flight work), then claim and process every due job. It returns
// the number of jobs processed this pass. Exposed (not just Run) so tests drive
// a single deterministic tick with an injected clock.
func (s *Scheduler) Tick(ctx context.Context) int {
	if s.Store == nil {
		return 0
	}
	now := s.now()

	// Recover jobs whose claim went stale (owner crashed mid-handler) so they are
	// re-claimed — at-least-once delivery. Best-effort: a failure here is logged,
	// never fatal to the tick.
	if n, err := s.Store.ReclaimStaleRunning(ctx, now.Add(-s.staleAfter())); err != nil {
		s.logger().Error("scheduler: reclaim stale running", "err", err)
	} else if n > 0 {
		s.logger().Warn("scheduler: reclaimed stale running jobs", "count", n)
	}

	claimed, err := s.Store.ClaimDueJobs(ctx, now, s.claimLimit(), s.instanceID())
	if err != nil {
		s.logger().Error("scheduler: claim due jobs", "err", err)
		return 0
	}
	for i := range claimed {
		s.process(ctx, claimed[i], now)
	}
	if len(claimed) > 0 {
		s.logger().Info("scheduler: processed jobs", "count", len(claimed))
	}
	return len(claimed)
}

// process runs one claimed job's handler and settles its terminal/next state.
func (s *Scheduler) process(ctx context.Context, job store.Job, now time.Time) {
	log := s.logger().With("job", job.ID, "handler", job.Handler, "vmid", vmidOf(job))

	// Missed-window policy: a job claimed far past its run_at was genuinely missed
	// (worker downtime). 'skip' abandons that occurrence; 'catch_up'/'run_late'
	// both still run once now (and a recurring job then reschedules forward, so a
	// long outage fires once, not the whole backlog).
	if job.MissedPolicy == "skip" && now.Sub(job.RunAt) > s.missedThreshold() {
		log.Info("scheduler: skipping missed occurrence (missed_policy=skip)",
			"late", now.Sub(job.RunAt).String())
		s.settleSkip(ctx, job, now, log)
		return
	}

	handler, ok := s.Handlers[job.Handler]
	if !ok {
		// An unknown handler can never succeed by retrying; fail it toward the
		// dead-letter so it is visible in the admin view rather than silently lost.
		s.fail(ctx, job, now, fmt.Errorf("no handler registered for %q", job.Handler), log)
		return
	}

	hctx, cancel := context.WithTimeout(ctx, s.handlerTimeout())
	err := handler(hctx, job)
	cancel()
	if err != nil {
		// Shutdown cancellation is not a handler failure: leave the row 'running'
		// so it is reclaimed and retried on the next boot (at-least-once), rather
		// than burning a retry attempt on a drain.
		if ctx.Err() != nil {
			log.Warn("scheduler: tick cancelled mid-handler; job left running for re-claim")
			return
		}
		s.fail(ctx, job, now, err, log)
		return
	}
	s.settleSuccess(ctx, job, now, log)
}

// settleSuccess marks a one-shot job succeeded, or reschedules a recurring job
// to its next cron boundary strictly after now (catch-up semantics).
func (s *Scheduler) settleSuccess(ctx context.Context, job store.Job, now time.Time, log *slog.Logger) {
	if job.Kind == "recurring" {
		s.reschedule(ctx, job, now, log)
		return
	}
	if err := s.Store.CompleteJob(ctx, job.ID); err != nil {
		log.Error("scheduler: complete job", "err", err)
	}
}

// settleSkip settles a skipped missed occurrence without running the handler: a
// recurring job advances to its next boundary; a one-shot is marked done.
func (s *Scheduler) settleSkip(ctx context.Context, job store.Job, now time.Time, log *slog.Logger) {
	if job.Kind == "recurring" {
		s.reschedule(ctx, job, now, log)
		return
	}
	if err := s.Store.CompleteJob(ctx, job.ID); err != nil {
		log.Error("scheduler: complete skipped job", "err", err)
	}
}

// reschedule computes a recurring job's next fire from its cron+timezone and
// returns it to 'scheduled'. A missing/invalid cron is a hard failure (a
// recurring job with no schedule can never fire again).
func (s *Scheduler) reschedule(ctx context.Context, job store.Job, now time.Time, log *slog.Logger) {
	if job.Cron == nil || *job.Cron == "" {
		s.fail(ctx, job, now, errors.New("recurring job has no cron expression"), log)
		return
	}
	tz := ""
	if job.Timezone != nil {
		tz = *job.Timezone
	}
	next, err := NextCron(*job.Cron, tz, now)
	if err != nil {
		s.fail(ctx, job, now, fmt.Errorf("compute next cron: %w", err), log)
		return
	}
	if err := s.Store.RescheduleRecurring(ctx, job.ID, next); err != nil {
		log.Error("scheduler: reschedule recurring", "err", err)
		return
	}
	log.Info("scheduler: recurring job rescheduled", "next_run_at", next.UTC().Format(time.RFC3339))
}

// fail records a handler error and lets the store decide retry vs dead-letter.
func (s *Scheduler) fail(ctx context.Context, job store.Job, now time.Time, cause error, log *slog.Logger) {
	retryAt := now.Add(s.backoff(job.Attempts))
	deadLettered, err := s.Store.FailJob(ctx, job.ID, cause.Error(), retryAt)
	if err != nil {
		log.Error("scheduler: fail job", "cause", cause.Error(), "err", err)
		return
	}
	if deadLettered {
		log.Error("scheduler: job dead-lettered (max attempts reached)", "cause", cause.Error())
	} else {
		log.Warn("scheduler: job failed, will retry",
			"cause", cause.Error(), "retry_at", retryAt.UTC().Format(time.RFC3339))
	}
}

// backoff is exponential in the pre-increment attempt count, capped at MaxBackoff.
func (s *Scheduler) backoff(attempts int) time.Duration {
	base := s.BaseBackoff
	if base <= 0 {
		base = defaultBaseBackoff
	}
	max := s.MaxBackoff
	if max <= 0 {
		max = defaultMaxBackoff
	}
	d := base
	for i := 0; i < attempts && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}

// NextCron returns the next activation of a 5-field standard cron expression
// strictly after `after`, evaluated in the given IANA timezone (empty → UTC).
// Timezone-aware so a "shut down at 19:00 Europe/Berlin" job survives DST.
func NextCron(expr, tz string, after time.Time) (time.Time, error) {
	sched, err := cron.ParseStandard(expr)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse cron %q: %w", expr, err)
	}
	loc := time.UTC
	if tz != "" {
		l, err := time.LoadLocation(tz)
		if err != nil {
			return time.Time{}, fmt.Errorf("load timezone %q: %w", tz, err)
		}
		loc = l
	}
	return sched.Next(after.In(loc)), nil
}

func vmidOf(job store.Job) int {
	if job.VMID != nil {
		return *job.VMID
	}
	return 0
}
