// Package lifecycle implements the auto-shutdown feature (ADR-0019) that rides the
// scheduler engine (ADR-0018): it materializes a durable Schedule into recurring
// `jobs` rows and provides the three idempotent, defensive handlers the engine
// dispatches (autoshutdown.stop / .warn / .start). Every scheduler-initiated
// power change re-reads ownership, gates on the guest's real PVE state, and is
// audited AS SYSTEM (actor_system="system:scheduler"), exactly as the reconciler.
package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/scheduler"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

// Handler dispatch keys — the same strings registered on the scheduler.
const (
	HandlerStop  = "autoshutdown.stop"
	HandlerWarn  = "autoshutdown.warn"
	HandlerStart = "autoshutdown.start"
)

// Audit action namespace for scheduler-initiated power changes (ADR-0018 §audit).
const (
	actionStop  = "guest.scheduler.stop"
	actionStart = "guest.scheduler.start"
)

const (
	defaultGrace        = 120 * time.Second
	defaultAwaitTimeout = 5 * time.Minute
	actorSystem         = "system:scheduler"
)

// AutoShutdown materializes schedules into jobs and runs the auto-shutdown
// handlers. Registry/Broker are nil-safe (no notifications/SSE without them);
// Now is injectable for deterministic tests.
type AutoShutdown struct {
	Store        store.Store
	PVE          proxmox.Client
	Registry     *tasks.Registry
	Broker       *events.Broker
	Log          *slog.Logger
	DefaultGrace time.Duration
	// AwaitTimeout bounds how long a stop/start handler waits for the PVE task to
	// settle before it verifies via status/current. Zero → defaultAwaitTimeout.
	AwaitTimeout time.Duration
	// Now is injectable for tests (defaults to time.Now).
	Now func() time.Time
}

// jobPayload is the per-job context carried in jobs.payload (jobs store only a
// VMID). Node/GuestType let a handler build the GuestRef; GraceSec + ShutdownTime
// carry the schedule's grace and human time for the shutdown/warning.
type jobPayload struct {
	ScheduleID   string `json:"schedule_id"`
	Node         string `json:"node"`
	GuestType    string `json:"guest_type"`
	GraceSec     int    `json:"grace_seconds"`
	ShutdownTime string `json:"shutdown_time,omitempty"`
}

func (s *AutoShutdown) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *AutoShutdown) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *AutoShutdown) grace() time.Duration {
	if s.DefaultGrace > 0 {
		return s.DefaultGrace
	}
	return defaultGrace
}

func (s *AutoShutdown) awaitTimeout() time.Duration {
	if s.AwaitTimeout > 0 {
		return s.AwaitTimeout
	}
	return defaultAwaitTimeout
}

// ---- materialization ----

// MaterializeForGuest rewrites a guest's auto-shutdown jobs from the currently
// resolved schedule (resource-over-project, opt-out). It first cancels the
// guest's existing jobs — the schedules table is the source of truth and jobs are
// its projection (ADR-0019) — then emits fresh recurring jobs, or none when the
// guest is opted out / has a disabled or absent schedule.
//
// NOTE: CancelJobsForVMID cancels ALL non-terminal jobs for the VMID. Today only
// autoshutdown.* jobs exist; when TTL jobs (ADR-0020) land, this must become a
// handler-scoped cancel so re-materializing a schedule does not drop TTL jobs.
func (s *AutoShutdown) MaterializeForGuest(ctx context.Context, own store.ResourceOwnership) error {
	// Atomic: cancel the old projection and emit the new one in a single
	// transaction, so a failure mid-sequence never leaves a guest with a partial
	// job set. WithTx is reentrant, so when a handler wraps upsert+materialize in
	// its own tx the schedule row and its jobs commit (or roll back) together.
	return s.Store.WithTx(ctx, func(txs store.Store) error {
		svc := *s
		svc.Store = txs
		return svc.materializeTx(ctx, own)
	})
}

// MaterializeForGuestWith runs materialization against a caller-supplied store —
// e.g. the transaction a handler already opened for the schedule upsert — so the
// schedule row and its job projection commit or roll back together. The caller
// owns the transaction boundary (this does not open its own).
func (s *AutoShutdown) MaterializeForGuestWith(ctx context.Context, st store.Store, own store.ResourceOwnership) error {
	svc := *s
	svc.Store = st
	return svc.materializeTx(ctx, own)
}

func (s *AutoShutdown) materializeTx(ctx context.Context, own store.ResourceOwnership) error {
	if _, err := s.Store.CancelJobsForVMID(ctx, own.VMID); err != nil {
		return fmt.Errorf("materialize: cancel existing jobs for vmid %d: %w", own.VMID, err)
	}
	if own.Status == "tombstoned" {
		return nil // gone: cancel only, emit nothing
	}
	sc, ok, err := s.resolve(ctx, own)
	if err != nil {
		return err
	}
	if !ok {
		return nil // opted out, disabled, or no schedule → no jobs
	}
	return s.emit(ctx, own, sc)
}

// MaterializeProject re-materializes every live guest in a project (a project
// schedule fans out to a job set per guest, honoring per-guest overrides/opt-outs).
// A per-guest failure is logged and skipped so one bad guest never blocks the rest.
func (s *AutoShutdown) MaterializeProject(ctx context.Context, projectID string) error {
	owns, err := s.Store.ListOwnershipByProject(ctx, projectID)
	if err != nil {
		return err
	}
	for _, own := range owns {
		if own.Status == "tombstoned" {
			continue
		}
		if err := s.MaterializeForGuest(ctx, own); err != nil {
			s.logger().Warn("materialize project: guest failed", "project", projectID, "vmid", own.VMID, "err", err)
		}
	}
	return nil
}

// resolve applies the ADR-0019 precedence: a resource-scope row wins outright (and
// an opt-out or disabled resource row emits nothing); otherwise the project
// schedule applies; a disabled or absent project schedule emits nothing.
func (s *AutoShutdown) resolve(ctx context.Context, own store.ResourceOwnership) (store.Schedule, bool, error) {
	res, err := s.Store.GetResourceSchedule(ctx, own.VMID)
	switch {
	case err == nil:
		if res.OptOut || !res.Enabled {
			return store.Schedule{}, false, nil
		}
		return *res, true, nil
	case errors.Is(err, store.ErrNotFound):
		// fall through to the project schedule
	default:
		return store.Schedule{}, false, err
	}

	proj, err := s.Store.GetProjectSchedule(ctx, own.TenantID, own.ProjectID)
	switch {
	case err == nil:
		if !proj.Enabled {
			return store.Schedule{}, false, nil
		}
		return *proj, true, nil
	case errors.Is(err, store.ErrNotFound):
		return store.Schedule{}, false, nil
	default:
		return store.Schedule{}, false, err
	}
}

// emit enqueues the recurring stop (+ warn, + optional start) jobs for one guest
// from a resolved schedule.
func (s *AutoShutdown) emit(ctx context.Context, own store.ResourceOwnership, sc store.Schedule) error {
	if len(sc.DaysOfWeek) == 0 {
		return fmt.Errorf("materialize vmid %d: schedule has no days_of_week", own.VMID)
	}
	hh, mm, err := parseHHMM(sc.ShutdownTime)
	if err != nil {
		return fmt.Errorf("materialize vmid %d: %w", own.VMID, err)
	}
	graceSec := sc.GraceSeconds
	if graceSec <= 0 {
		graceSec = int(s.grace().Seconds())
	}
	payload, err := json.Marshal(jobPayload{
		ScheduleID: sc.ID, Node: own.Node, GuestType: own.GuestType,
		GraceSec: graceSec, ShutdownTime: sc.ShutdownTime,
	})
	if err != nil {
		return fmt.Errorf("materialize vmid %d: marshal payload: %w", own.VMID, err)
	}
	tenantID, projectID, vmid, tz := own.TenantID, own.ProjectID, own.VMID, sc.Timezone

	// Stop: catch_up — a missed shutdown powers the guest down at the next tick.
	if err := s.enqueue(ctx, HandlerStop, cronExpr(hh, mm, sc.DaysOfWeek), tz, "catch_up", payload, &tenantID, &projectID, &vmid); err != nil {
		return err
	}
	// Warn (T-15m): run_late — a late heads-up is still worth sending.
	wh, wm, wdays := warnTime(hh, mm, sc.DaysOfWeek)
	if err := s.enqueue(ctx, HandlerWarn, cronExpr(wh, wm, wdays), tz, "run_late", payload, &tenantID, &projectID, &vmid); err != nil {
		return err
	}
	// Start (optional): catch_up.
	if sc.AutoStartTime != nil && *sc.AutoStartTime != "" {
		sh, sm, err := parseHHMM(*sc.AutoStartTime)
		if err != nil {
			return fmt.Errorf("materialize vmid %d: auto_start_time: %w", own.VMID, err)
		}
		if err := s.enqueue(ctx, HandlerStart, cronExpr(sh, sm, sc.DaysOfWeek), tz, "catch_up", payload, &tenantID, &projectID, &vmid); err != nil {
			return err
		}
	}
	return nil
}

func (s *AutoShutdown) enqueue(ctx context.Context, handler, cron, tz, missed string, payload []byte, tid, pid *string, vmid *int) error {
	next, err := scheduler.NextCron(cron, tz, s.now())
	if err != nil {
		return fmt.Errorf("materialize: next cron for %s: %w", handler, err)
	}
	c, z := cron, tz
	if _, err := s.Store.EnqueueJob(ctx, store.EnqueueJobParams{
		Kind: "recurring", Handler: handler,
		TenantID: tid, ProjectID: pid, VMID: vmid,
		Payload: payload, Cron: &c, Timezone: &z, RunAt: next,
		MissedPolicy: missed,
	}); err != nil {
		return fmt.Errorf("materialize: enqueue %s: %w", handler, err)
	}
	return nil
}

// ---- handlers ----

// AutoShutdownStop powers a guest down: observe → decide → act → verify. Already
// stopped is a logged no-op (recorded as a skipped success). Otherwise it audits
// the intent (fail-closed, before mutating), shuts down with the schedule's grace,
// waits for the task, and confirms via status/current before marking auto_stopped.
func (s *AutoShutdown) AutoShutdownStop(ctx context.Context, job store.Job) error {
	own, pl, ref, proceed, err := s.load(ctx, job)
	if err != nil || !proceed {
		return err
	}
	log := s.logger().With("handler", HandlerStop, "vmid", ref.VMID, "job", job.ID)

	st, err := s.PVE.GuestStatus(ctx, ref)
	if err != nil {
		return err // transient/infra error → retry (do not mutate)
	}
	if st.Status == "stopped" {
		log.Info("autoshutdown: guest already stopped, no-op")
		s.auditSkip(ctx, job, actionStop, pl.ScheduleID, "already_stopped")
		return nil
	}

	graceSec := pl.GraceSec
	if graceSec <= 0 {
		graceSec = int(s.grace().Seconds())
	}
	auditID, err := s.beginAudit(ctx, job, actionStop)
	if err != nil {
		return fmt.Errorf("autoshutdown: audit intent: %w", err) // fail closed
	}
	upid, err := s.PVE.GuestShutdown(ctx, ref, graceSec)
	if err != nil {
		s.finishAudit(ctx, auditID, "error", pl.ScheduleID, job.ID, map[string]any{"error": err.Error()})
		return fmt.Errorf("autoshutdown: shutdown vmid %d: %w", ref.VMID, err)
	}
	s.track(upid, "Auto-shutdown", "stopping", own)
	// Wait long enough to cover the full PVE-side grace window before verifying.
	s.await(ctx, upid, s.awaitFor(graceSec))

	after, err := s.PVE.GuestStatus(ctx, ref)
	if err != nil {
		s.finishAudit(ctx, auditID, "error", pl.ScheduleID, job.ID, map[string]any{"error": err.Error()})
		return err
	}
	if after.Status != "stopped" {
		e := fmt.Errorf("guest still running after shutdown (grace %ds)", graceSec)
		s.finishAudit(ctx, auditID, "error", pl.ScheduleID, job.ID, map[string]any{"error": e.Error()})
		return e
	}
	if err := s.Store.SetAutoStopped(ctx, ref.VMID, true); err != nil {
		log.Warn("autoshutdown: set auto_stopped", "err", err)
	}
	s.finishAudit(ctx, auditID, "success", pl.ScheduleID, job.ID, map[string]any{"grace_seconds": graceSec})
	log.Info("autoshutdown: guest stopped")
	return nil
}

// AutoShutdownStart powers a guest back on ONLY if the scheduler stopped it
// (auto_stopped). A user-stopped guest is left down. Already running clears the
// marker and is a no-op success.
func (s *AutoShutdown) AutoShutdownStart(ctx context.Context, job store.Job) error {
	own, pl, ref, proceed, err := s.load(ctx, job)
	if err != nil || !proceed {
		return err
	}
	log := s.logger().With("handler", HandlerStart, "vmid", ref.VMID, "job", job.ID)

	if !own.AutoStopped {
		log.Info("autoshutdown: guest not scheduler-stopped, skipping auto-start")
		return nil
	}

	st, err := s.PVE.GuestStatus(ctx, ref)
	if err != nil {
		return err
	}
	if st.Status == "running" {
		if err := s.Store.SetAutoStopped(ctx, ref.VMID, false); err != nil {
			log.Warn("autoshutdown: clear auto_stopped", "err", err)
		}
		s.auditSkip(ctx, job, actionStart, pl.ScheduleID, "already_running")
		return nil
	}

	auditID, err := s.beginAudit(ctx, job, actionStart)
	if err != nil {
		return fmt.Errorf("autoshutdown: audit intent: %w", err)
	}
	upid, err := s.PVE.GuestAction(ctx, ref, "start")
	if err != nil {
		s.finishAudit(ctx, auditID, "error", pl.ScheduleID, job.ID, map[string]any{"error": err.Error()})
		return fmt.Errorf("autoshutdown: start vmid %d: %w", ref.VMID, err)
	}
	s.track(upid, "Auto-start", "starting", own)
	s.await(ctx, upid, s.awaitTimeout())

	if err := s.Store.SetAutoStopped(ctx, ref.VMID, false); err != nil {
		log.Warn("autoshutdown: clear auto_stopped", "err", err)
	}
	s.finishAudit(ctx, auditID, "success", pl.ScheduleID, job.ID, nil)
	log.Info("autoshutdown: guest started")
	return nil
}

// AutoShutdownWarn sends the T-15m heads-up: a standalone notification plus the
// tenant-scoped SSE schedule_warning frame. No PVE call, no audit — it changes no
// guest state.
func (s *AutoShutdown) AutoShutdownWarn(ctx context.Context, job store.Job) error {
	_, pl, ref, proceed, err := s.load(ctx, job)
	if err != nil || !proceed {
		return err
	}
	title := "Scheduled shutdown soon"
	detail := fmt.Sprintf("%s/%d will auto-shut-down at %s", ref.Type, ref.VMID, pl.ShutdownTime)
	scheduledAt := s.now().Add(warnLead)

	// Deliver ONLY via the tenant-scoped SSE frame (events.deliver filters it by
	// owned VMID). We deliberately do NOT write to tasks.Registry's notification
	// ring: GET /api/notifications returns that ring unscoped to every
	// authenticated user, so fanning a guest's identity + shutdown time into it
	// would leak one tenant's activity cross-tenant (tenancy iron rule #1). The
	// ring can carry warnings again once it is itself tenant-scoped.
	if s.Broker != nil {
		s.Broker.Publish(events.Event{Name: "schedule_warning", Data: types.ScheduleWarningEvent{
			VMID: ref.VMID, Node: ref.Node, GuestType: ref.Type,
			Kind: "autoshutdown", Title: title, Detail: detail, ScheduledAt: scheduledAt,
		}})
	}
	s.logger().Info("autoshutdown: warning sent", "vmid", ref.VMID, "shutdown_time", pl.ShutdownTime)
	return nil
}

// ---- shared handler helpers ----

// load is the defensive preamble every handler runs: decode the payload and
// re-read ownership. If the owning guest is gone or tombstoned it cancels the
// owner's remaining jobs and returns proceed=false with a nil error (owner gone is
// success, not failure — ADR-0018). The GuestRef is built from the ownership row
// (the current DB truth), not the possibly-stale payload node.
func (s *AutoShutdown) load(ctx context.Context, job store.Job) (own store.ResourceOwnership, pl jobPayload, ref proxmox.GuestRef, proceed bool, err error) {
	if job.VMID == nil {
		return own, pl, ref, false, fmt.Errorf("autoshutdown: job %s has no vmid", job.ID)
	}
	vmid := *job.VMID
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &pl); err != nil {
			return own, pl, ref, false, fmt.Errorf("autoshutdown: decode payload: %w", err)
		}
	}
	o, err := s.Store.GetOwnershipByVMID(ctx, vmid)
	if errors.Is(err, store.ErrNotFound) {
		s.cancelGone(ctx, vmid, "owner gone")
		return own, pl, ref, false, nil
	}
	if err != nil {
		return own, pl, ref, false, err
	}
	if o.Status == "tombstoned" {
		s.cancelGone(ctx, vmid, "owner tombstoned")
		return *o, pl, ref, false, nil
	}
	ref = proxmox.GuestRef{Node: o.Node, Type: o.GuestType, VMID: vmid}
	return *o, pl, ref, true, nil
}

func (s *AutoShutdown) cancelGone(ctx context.Context, vmid int, reason string) {
	if _, err := s.Store.CancelJobsForVMID(ctx, vmid); err != nil {
		s.logger().Warn("autoshutdown: cancel jobs for gone owner", "vmid", vmid, "reason", reason, "err", err)
		return
	}
	s.logger().Info("autoshutdown: owner gone, cancelled its jobs", "vmid", vmid, "reason", reason)
}

// track registers the PVE task + announces it (nil-safe on Registry/Broker).
func (s *AutoShutdown) track(upid proxmox.UPID, action, transitional string, own store.ResourceOwnership) {
	res := types.TaskResource{Type: own.GuestType, VMID: own.VMID, Node: own.Node}
	if s.Registry != nil {
		s.Registry.Track(upid, action, transitional, res)
	}
	if s.Broker != nil {
		s.Broker.Publish(events.Event{Name: "task", Data: types.TaskEvent{
			UPID: string(upid), Action: action, Status: "running", Resource: &res,
		}})
	}
}

// await waits up to `bound` for a tracked PVE task to settle so the follow-up
// status/current read reflects the outcome. The wait is best-effort: the
// authoritative check is status/current, not the task result (lifecycle.md §2), so
// a wait timeout is not an error — the caller's status re-read decides. The bound
// MUST exceed the guest's grace window (GuestShutdown delegates graceful→force to
// PVE over `timeout=graceSec`), or the wait would give up while the shutdown task
// is still legitimately mid-grace.
func (s *AutoShutdown) await(ctx context.Context, upid proxmox.UPID, bound time.Duration) {
	if s.Registry == nil {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	if _, err := s.Registry.AwaitCompletion(wctx, upid); err != nil {
		s.logger().Debug("autoshutdown: await task settle", "upid", string(upid), "err", err)
	}
}

// awaitFor derives the await bound for a stop whose PVE-side grace window is
// graceSec: the whole window plus a settle margin, floored at the configured
// default. graceSec is capped at input (maxGraceSeconds), so this stays well
// under the scheduler's per-handler timeout.
func (s *AutoShutdown) awaitFor(graceSec int) time.Duration {
	d := time.Duration(graceSec)*time.Second + 90*time.Second
	if base := s.awaitTimeout(); d < base {
		return base
	}
	return d
}

// beginAudit writes the fail-closed intent as system:scheduler (ADR-0018): a NULL
// actor_user_id plus actor_system, targeting the guest.
func (s *AutoShutdown) beginAudit(ctx context.Context, job store.Job, action string) (string, error) {
	tt := "guest"
	sys := actorSystem
	var target *string
	if job.VMID != nil {
		v := strconv.Itoa(*job.VMID)
		target = &v
	}
	return s.Store.InsertAuditIntent(ctx, store.AuditIntent{
		ActorUserID: nil,
		ActorSystem: &sys,
		TenantID:    job.TenantID,
		ProjectID:   job.ProjectID,
		Action:      action,
		TargetType:  &tt,
		TargetID:    target,
	})
}

// finishAudit finalizes an intent row with the outcome + a detail carrying the
// schedule and job ids (ADR-0018).
func (s *AutoShutdown) finishAudit(ctx context.Context, id, outcome, scheduleID, jobID string, extra map[string]any) {
	detail := map[string]any{"schedule_id": scheduleID, "job_id": jobID}
	for k, v := range extra {
		detail[k] = v
	}
	b, err := json.Marshal(detail)
	if err != nil {
		b = nil
	}
	if err := s.Store.FinalizeAudit(ctx, id, outcome, b); err != nil {
		s.logger().Warn("autoshutdown: finalize audit", "audit_id", id, "err", err)
	}
}

// auditSkip records a no-op (already in the desired state) as a skipped success,
// still through the intent→finalize spine so the activity log is honest.
func (s *AutoShutdown) auditSkip(ctx context.Context, job store.Job, action, scheduleID, reason string) {
	id, err := s.beginAudit(ctx, job, action)
	if err != nil {
		s.logger().Warn("autoshutdown: audit intent (skip)", "err", err)
		return
	}
	s.finishAudit(ctx, id, "success", scheduleID, job.ID, map[string]any{"skipped": true, "reason": reason})
}
