package lifecycle

// TTL / ephemeral resources (ADR-0020) rides the same scheduler engine (ADR-0018)
// as auto-shutdown. A guest's optional expiry is materialized into three one-shot
// `jobs`: two heads-up warnings (T-24h, T-1h) and the expire itself. Expiry is
// `stop` (reversible: powered off + marked expired) or `delete` (a real Proxmox
// destroy that reuses the existing ownership tombstone-on-destroy path and leaves
// a tombstone audit row carrying a full config snapshot). Every scheduler-
// initiated mutation is audited AS SYSTEM (actor_system="system:scheduler"),
// exactly like auto-shutdown.

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
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

// Handler dispatch keys — the strings registered on the scheduler for TTL jobs.
// The "ttl." prefix is load-bearing: MaterializeForGuest cancels ONLY these when
// it rewrites a guest's TTL job set, leaving auto-shutdown jobs untouched (and
// vice-versa, ADR-0020).
const (
	HandlerTTLWarn   = "ttl.warn"
	HandlerTTLExpire = "ttl.expire"
	ttlJobPrefix     = "ttl."
)

// Audit action namespace for scheduler-initiated TTL expiry (ADR-0020 §audit).
const (
	actionTTLStop   = "guest.ttl.stop"
	actionTTLDelete = "guest.ttl.delete"
)

// DefaultMaxTTL is the policy ceiling when a project has no stored TTL policy
// (matches the migration default, project_ttl_policy.max_ttl = 30 days).
const DefaultMaxTTL = 30 * 24 * time.Hour

// warn tiers: how far ahead of expiry each heads-up fires.
const (
	warnLead24h = 24 * time.Hour
	warnLead1h  = 1 * time.Hour
)

// TTL materializes a guest's expiry into jobs and runs the warn/expire handlers.
// Registry/Broker are nil-safe (no task tracking / SSE without them); Now is
// injectable for deterministic tests. It mirrors the AutoShutdown service.
type TTL struct {
	Store        store.Store
	PVE          proxmox.Client
	Registry     *tasks.Registry
	Broker       *events.Broker
	Log          *slog.Logger
	DefaultGrace time.Duration
	// AwaitTimeout bounds how long an expire handler waits for a PVE task to
	// settle before verifying via status/current. Zero → defaultAwaitTimeout.
	AwaitTimeout time.Duration
	// Now is injectable for tests (defaults to time.Now).
	Now func() time.Time
}

// ttlJobPayload is the per-job context carried in jobs.payload (jobs store only a
// VMID). Node/GuestType let a handler build the GuestRef; Which marks a warn job's
// tier; Action/TTLID identify the expiry the job serves.
type ttlJobPayload struct {
	Node      string `json:"node"`
	GuestType string `json:"guest_type"`
	TTLID     string `json:"ttl_id"`
	Action    string `json:"action"`          // "stop" | "delete"
	Which     string `json:"which,omitempty"` // "24h" | "1h" (warn jobs only)
}

func (s *TTL) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

func (s *TTL) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *TTL) grace() time.Duration {
	if s.DefaultGrace > 0 {
		return s.DefaultGrace
	}
	return defaultGrace
}

func (s *TTL) awaitTimeout() time.Duration {
	if s.AwaitTimeout > 0 {
		return s.AwaitTimeout
	}
	return defaultAwaitTimeout
}

// ---- materialization ----

// MaterializeForGuest rewrites a guest's TTL jobs from its currently stored TTL.
// It cancels ONLY the guest's ttl.* jobs (never its auto-shutdown jobs) then
// emits the fresh warn + expire set, or none when the guest has no TTL / is
// tombstoned. WithTx is reentrant, so wrapping it in a handler's own transaction
// commits the TTL row and its jobs together.
func (s *TTL) MaterializeForGuest(ctx context.Context, own store.ResourceOwnership) error {
	return s.Store.WithTx(ctx, func(txs store.Store) error {
		svc := *s
		svc.Store = txs
		return svc.materializeTx(ctx, own)
	})
}

// MaterializeForGuestWith runs materialization against a caller-supplied store —
// e.g. the transaction a handler already opened for the TTL upsert — so the TTL
// row and its job projection commit or roll back together. The caller owns the
// transaction boundary (this does not open its own).
func (s *TTL) MaterializeForGuestWith(ctx context.Context, st store.Store, own store.ResourceOwnership) error {
	svc := *s
	svc.Store = st
	return svc.materializeTx(ctx, own)
}

func (s *TTL) materializeTx(ctx context.Context, own store.ResourceOwnership) error {
	// Cancel ONLY this feature's jobs (handler prefix), so re-materializing a TTL
	// never drops a guest's auto-shutdown jobs (ADR-0020).
	if _, err := s.Store.CancelJobsForVMIDByPrefix(ctx, own.VMID, ttlJobPrefix); err != nil {
		return fmt.Errorf("materialize ttl: cancel existing jobs for vmid %d: %w", own.VMID, err)
	}
	if own.Status == "tombstoned" {
		return nil // gone: cancel only, emit nothing
	}
	ttl, err := s.Store.GetTTL(ctx, own.VMID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // no TTL → no jobs
	}
	if err != nil {
		return err
	}
	return s.emit(ctx, own, ttl)
}

// emit enqueues the two warn jobs (skipping any whose fire time is already past
// or whose flag is already set) plus the expire job. All three are one_shot with
// missed_policy run_late — a missed warning or expiry is still worth firing late.
func (s *TTL) emit(ctx context.Context, own store.ResourceOwnership, ttl *store.TTL) error {
	tid, pid, vmid := own.TenantID, own.ProjectID, own.VMID
	now := s.now()

	warn := func(which string, lead time.Duration, already bool) error {
		at := ttl.ExpiresAt.Add(-lead)
		if already || !at.After(now) {
			return nil // already warned, or the tier is in the past → skip
		}
		return s.enqueueOneShot(ctx, HandlerTTLWarn, at, ttlJobPayload{
			Node: own.Node, GuestType: own.GuestType, TTLID: ttl.ID, Action: ttl.Action, Which: which,
		}, tid, pid, vmid)
	}
	if err := warn("24h", warnLead24h, ttl.Warned24h); err != nil {
		return err
	}
	if err := warn("1h", warnLead1h, ttl.Warned1h); err != nil {
		return err
	}
	// Expire: always enqueued (a past expiry fires immediately via run_late).
	return s.enqueueOneShot(ctx, HandlerTTLExpire, ttl.ExpiresAt, ttlJobPayload{
		Node: own.Node, GuestType: own.GuestType, TTLID: ttl.ID, Action: ttl.Action,
	}, tid, pid, vmid)
}

func (s *TTL) enqueueOneShot(ctx context.Context, handler string, runAt time.Time, pl ttlJobPayload, tid, pid string, vmid int) error {
	payload, err := json.Marshal(pl)
	if err != nil {
		return fmt.Errorf("materialize ttl: marshal payload: %w", err)
	}
	t, p, v := tid, pid, vmid
	if _, err := s.Store.EnqueueJob(ctx, store.EnqueueJobParams{
		Kind: "one_shot", Handler: handler,
		TenantID: &t, ProjectID: &p, VMID: &v,
		Payload: payload, RunAt: runAt, MissedPolicy: "run_late",
	}); err != nil {
		return fmt.Errorf("materialize ttl: enqueue %s: %w", handler, err)
	}
	return nil
}

// ---- extend ----

// CapExtension computes a TTL's extended expiry: expiresAt + original, capped at
// now + maxTTL (so repeated extends can never keep a guest past the project's
// ceiling, ADR-0020).
func CapExtension(now, expiresAt time.Time, original, maxTTL time.Duration) time.Time {
	newExpiry := expiresAt.Add(original)
	ceiling := now.Add(maxTTL)
	if newExpiry.After(ceiling) {
		return ceiling
	}
	return newExpiry
}

// ExtendTTL extends a guest's expiry by one original_duration, capped at the
// project max_ttl (absent policy → DefaultMaxTTL), resets the warning flags, and
// re-materializes the jobs — all atomically. Returns the new expiry.
func (s *TTL) ExtendTTL(ctx context.Context, own store.ResourceOwnership, ttl *store.TTL) (time.Time, error) {
	maxTTL := DefaultMaxTTL
	pol, err := s.Store.GetProjectTTLPolicy(ctx, own.TenantID, own.ProjectID)
	switch {
	case err == nil:
		maxTTL = pol.MaxTTL
	case errors.Is(err, store.ErrNotFound):
		// keep DefaultMaxTTL
	default:
		return time.Time{}, err
	}
	newExpiry := CapExtension(s.now(), ttl.ExpiresAt, ttl.OriginalDuration, maxTTL)

	err = s.Store.WithTx(ctx, func(txs store.Store) error {
		if e := txs.UpdateTTLExpiry(ctx, own.VMID, newExpiry); e != nil {
			return e
		}
		svc := *s
		svc.Store = txs
		return svc.materializeTx(ctx, own)
	})
	if err != nil {
		return time.Time{}, err
	}
	return newExpiry, nil
}

// ---- handlers ----

// TTLWarn sends one heads-up (T-24h or T-1h): the tenant-scoped SSE ttl_warning
// frame. Idempotent — if the matching warned flag is already set it is a no-op.
// No PVE call, no audit (it changes no guest state). Like auto-shutdown warnings
// it deliberately does NOT write to the global notification ring (that ring is
// unscoped, so it would leak one tenant's activity cross-tenant); SSE only.
func (s *TTL) TTLWarn(ctx context.Context, job store.Job) error {
	_, pl, ref, proceed, err := s.load(ctx, job)
	if err != nil || !proceed {
		return err
	}
	ttl, err := s.Store.GetTTL(ctx, ref.VMID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // TTL cleared before the warn fired: nothing to announce
	}
	if err != nil {
		return err
	}
	which := pl.Which
	if (which == "24h" && ttl.Warned24h) || (which == "1h" && ttl.Warned1h) {
		return nil // already warned (at-least-once double-send guard)
	}
	if s.Broker != nil {
		s.Broker.Publish(events.Event{Name: "ttl_warning", Data: types.TtlWarningEvent{
			VMID: ref.VMID, Node: ref.Node, GuestType: ref.Type, Which: which,
			ExpiresAt: ttl.ExpiresAt, Action: ttl.Action,
		}})
	}
	if err := s.Store.SetTTLWarned(ctx, ref.VMID, which); err != nil {
		return err
	}
	s.logger().Info("ttl: warning sent", "vmid", ref.VMID, "which", which, "expires_at", ttl.ExpiresAt)
	return nil
}

// TTLExpire fires a guest's expiry. It re-reads the TTL + ownership (defensive):
// a TTL cleared out of band is a no-op; a gone/tombstoned owner self-cancels.
// Dispatches on the stored action to the reversible stop or the irreversible
// delete path.
func (s *TTL) TTLExpire(ctx context.Context, job store.Job) error {
	own, _, ref, proceed, err := s.load(ctx, job)
	if err != nil || !proceed {
		return err
	}
	ttl, err := s.Store.GetTTL(ctx, ref.VMID)
	if errors.Is(err, store.ErrNotFound) {
		return nil // TTL cleared before expiry fired: nothing to do
	}
	if err != nil {
		return err
	}
	// Race guard: an Extend that landed after this job was claimed (so the
	// prefix-cancel in re-materialization could not stop an already-running job)
	// pushes expires_at into the future. Re-check against the fresh row and bail —
	// never stop, and NEVER destroy, a guest whose TTL was just extended.
	if ttl.ExpiresAt.After(s.now()) {
		s.logger().Info("ttl: expiry no-op, TTL was extended past now",
			"vmid", ref.VMID, "expires_at", ttl.ExpiresAt)
		return nil
	}
	switch ttl.Action {
	case "stop":
		return s.expireStop(ctx, job, own, ref, ttl)
	case "delete":
		return s.expireDelete(ctx, job, own, ref, ttl)
	default:
		return fmt.Errorf("ttl: unknown expiry action %q for vmid %d", ttl.Action, ref.VMID)
	}
}

// expireStop powers a guest down (graceful→force) and marks it expired — a
// reversible state distinct from a user-stop and an auto-shutdown stop. Already
// stopped still marks expired (it reached its TTL) as a skipped success. Audited
// AS SYSTEM guest.ttl.stop, intent before mutation.
func (s *TTL) expireStop(ctx context.Context, job store.Job, own store.ResourceOwnership, ref proxmox.GuestRef, ttl *store.TTL) error {
	log := s.logger().With("handler", HandlerTTLExpire, "action", "stop", "vmid", ref.VMID, "job", job.ID)

	st, err := s.PVE.GuestStatus(ctx, ref)
	if err != nil {
		return err // transient/infra error → retry (do not mutate)
	}

	auditID, err := s.beginAudit(ctx, job, actionTTLStop)
	if err != nil {
		return fmt.Errorf("ttl: audit intent: %w", err) // fail closed
	}

	now := s.now()
	if st.Status == "stopped" {
		if err := s.Store.SetExpiredAt(ctx, ref.VMID, &now); err != nil {
			s.finishAudit(ctx, auditID, "error", ttl.ID, job.ID, map[string]any{"error": err.Error()})
			return err
		}
		s.finishAudit(ctx, auditID, "success", ttl.ID, job.ID, map[string]any{"skipped": true, "reason": "already_stopped"})
		log.Info("ttl: guest already stopped, marked expired")
		return nil
	}

	graceSec := int(s.grace().Seconds())
	upid, err := s.PVE.GuestShutdown(ctx, ref, graceSec)
	if err != nil {
		s.finishAudit(ctx, auditID, "error", ttl.ID, job.ID, map[string]any{"error": err.Error()})
		return fmt.Errorf("ttl: shutdown vmid %d: %w", ref.VMID, err)
	}
	s.track(upid, "TTL expiry (stop)", "stopping", own)
	s.await(ctx, upid, s.awaitFor(graceSec))

	after, err := s.PVE.GuestStatus(ctx, ref)
	if err != nil {
		s.finishAudit(ctx, auditID, "error", ttl.ID, job.ID, map[string]any{"error": err.Error()})
		return err
	}
	if after.Status != "stopped" {
		e := fmt.Errorf("guest still running after shutdown (grace %ds)", graceSec)
		s.finishAudit(ctx, auditID, "error", ttl.ID, job.ID, map[string]any{"error": e.Error()})
		return e
	}
	if err := s.Store.SetExpiredAt(ctx, ref.VMID, &now); err != nil {
		s.finishAudit(ctx, auditID, "error", ttl.ID, job.ID, map[string]any{"error": err.Error()})
		return err
	}
	s.finishAudit(ctx, auditID, "success", ttl.ID, job.ID, map[string]any{"grace_seconds": graceSec})
	log.Info("ttl: guest stopped and marked expired")
	return nil
}

// expireDelete performs the irreversible destroy: snapshot the guest config,
// ensure it is stopped (graceful→force), destroy it (purge), then — on a
// confirmed successful destroy — reuse the ownership tombstone-on-destroy path
// and cancel the guest's jobs. The tombstone audit (guest.ttl.delete, AS SYSTEM)
// carries the full config snapshot + ttl_id so the destroy is reconstructable.
// Fail-closed audit ordering: intent before any mutation.
func (s *TTL) expireDelete(ctx context.Context, job store.Job, own store.ResourceOwnership, ref proxmox.GuestRef, ttl *store.TTL) error {
	log := s.logger().With("handler", HandlerTTLExpire, "action", "delete", "vmid", ref.VMID, "job", job.ID)

	st, err := s.PVE.GuestStatus(ctx, ref)
	if err != nil {
		return err
	}
	// Snapshot BEFORE any mutation, while the guest still exists, so the tombstone
	// carries what was lost even if the destroy path later errors.
	snapshot := s.snapshot(ctx, ref, own, ttl, st)

	auditID, err := s.beginAudit(ctx, job, actionTTLDelete)
	if err != nil {
		return fmt.Errorf("ttl: audit intent: %w", err) // fail closed
	}

	graceSec := int(s.grace().Seconds())
	if st.Status != "stopped" {
		upid, err := s.PVE.GuestShutdown(ctx, ref, graceSec)
		if err != nil {
			s.finishAudit(ctx, auditID, "error", ttl.ID, job.ID, map[string]any{"error": err.Error()})
			return fmt.Errorf("ttl: shutdown before delete vmid %d: %w", ref.VMID, err)
		}
		s.track(upid, "TTL expiry (delete)", "stopping", own)
		s.await(ctx, upid, s.awaitFor(graceSec))
		after, err := s.PVE.GuestStatus(ctx, ref)
		if err != nil {
			s.finishAudit(ctx, auditID, "error", ttl.ID, job.ID, map[string]any{"error": err.Error()})
			return err
		}
		if after.Status != "stopped" {
			e := fmt.Errorf("guest still running after shutdown (grace %ds); not destroying", graceSec)
			s.finishAudit(ctx, auditID, "error", ttl.ID, job.ID, map[string]any{"error": e.Error()})
			return e
		}
	}

	upid, err := s.PVE.DeleteGuest(ctx, ref, true)
	if err != nil {
		s.finishAudit(ctx, auditID, "error", ttl.ID, job.ID, map[string]any{"error": err.Error()})
		return fmt.Errorf("ttl: destroy vmid %d: %w", ref.VMID, err)
	}
	s.track(upid, "TTL expiry (delete)", "deleting", own)
	if !s.awaitDestroy(ctx, upid) {
		e := fmt.Errorf("destroy task did not complete successfully for vmid %d", ref.VMID)
		s.finishAudit(ctx, auditID, "error", ttl.ID, job.ID, map[string]any{"error": e.Error()})
		return e
	}

	// Confirmed destroyed: reuse the existing tombstone-on-destroy lifecycle
	// (ADR-0010) so TTL-delete and user-delete converge on one ownership path.
	if err := s.Store.TombstoneOwnership(ctx, own.ID); err != nil {
		s.finishAudit(ctx, auditID, "error", ttl.ID, job.ID, map[string]any{"error": err.Error()})
		return fmt.Errorf("ttl: tombstone ownership vmid %d: %w", ref.VMID, err)
	}
	// Cancel the guest's remaining jobs at the destroy choke-point so no orphaned
	// job acts on a reused VMID. Best-effort; the defensive re-read is the backstop.
	if _, err := s.Store.CancelJobsForVMID(ctx, ref.VMID); err != nil {
		log.Warn("ttl: cancel jobs after destroy", "err", err)
	}
	s.finishAudit(ctx, auditID, "success", ttl.ID, job.ID, map[string]any{
		"action": "delete", "config_snapshot": snapshot,
	})
	log.Info("ttl: guest destroyed and ownership tombstoned")
	return nil
}

// snapshot builds the tombstone's config record. It reads the guest's raw PVE
// config (best-effort — a read failure is recorded, never fabricated) plus the
// live status and the ownership/TTL context, so an operator can reconstruct what
// was destroyed. NOTE: PVE returns cloud-init passwords already masked; no
// Proxcloud token secret is ever part of a guest config, so nothing here needs
// redaction. Limitation: this is the current config, not a point-in-time backup —
// disk contents are not captured (a destroy with purge is still irreversible).
func (s *TTL) snapshot(ctx context.Context, ref proxmox.GuestRef, own store.ResourceOwnership, ttl *store.TTL, st *proxmox.GuestStatusInfo) map[string]any {
	snap := map[string]any{
		"vmid":       ref.VMID,
		"node":       own.Node,
		"guest_type": own.GuestType,
		"tenant_id":  own.TenantID,
		"project_id": own.ProjectID,
		"ttl_id":     ttl.ID,
		"action":     ttl.Action,
		"expires_at": ttl.ExpiresAt,
	}
	if st != nil {
		snap["name"] = st.Name
		snap["status"] = st.Status
	}
	cfg, err := s.PVE.GuestConfig(ctx, ref)
	if err != nil {
		snap["config_error"] = err.Error()
	} else {
		snap["config"] = cfg
	}
	return snap
}

// ---- shared handler helpers ----

// load is the defensive preamble every handler runs: decode the payload and
// re-read ownership. If the owning guest is gone or tombstoned it cancels the
// owner's remaining jobs and returns proceed=false with a nil error (owner gone is
// success, not failure — ADR-0018). The GuestRef is built from the ownership row
// (the current DB truth), not the possibly-stale payload node.
func (s *TTL) load(ctx context.Context, job store.Job) (own store.ResourceOwnership, pl ttlJobPayload, ref proxmox.GuestRef, proceed bool, err error) {
	if job.VMID == nil {
		return own, pl, ref, false, fmt.Errorf("ttl: job %s has no vmid", job.ID)
	}
	vmid := *job.VMID
	if len(job.Payload) > 0 {
		if err := json.Unmarshal(job.Payload, &pl); err != nil {
			return own, pl, ref, false, fmt.Errorf("ttl: decode payload: %w", err)
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

func (s *TTL) cancelGone(ctx context.Context, vmid int, reason string) {
	if _, err := s.Store.CancelJobsForVMID(ctx, vmid); err != nil {
		s.logger().Warn("ttl: cancel jobs for gone owner", "vmid", vmid, "reason", reason, "err", err)
		return
	}
	s.logger().Info("ttl: owner gone, cancelled its jobs", "vmid", vmid, "reason", reason)
}

// track registers the PVE task + announces it (nil-safe on Registry/Broker).
func (s *TTL) track(upid proxmox.UPID, action, transitional string, own store.ResourceOwnership) {
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

// await waits up to bound for a tracked PVE task to settle (best-effort — the
// authoritative check is the follow-up status/current read, not the task result).
func (s *TTL) await(ctx context.Context, upid proxmox.UPID, bound time.Duration) {
	if s.Registry == nil {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, bound)
	defer cancel()
	if _, err := s.Registry.AwaitCompletion(wctx, upid); err != nil {
		s.logger().Debug("ttl: await task settle", "upid", string(upid), "err", err)
	}
}

// awaitDestroy waits for a destroy task and reports whether it SUCCEEDED — the
// gate before tombstoning ownership: a delete only releases the VMID on a
// confirmed vzdestroy/qmdestroy. When there is no Registry (unit tests) the task
// cannot be observed, so it proceeds best-effort on the no-error submit.
func (s *TTL) awaitDestroy(ctx context.Context, upid proxmox.UPID) bool {
	if s.Registry == nil {
		return true
	}
	wctx, cancel := context.WithTimeout(ctx, s.awaitTimeout())
	defer cancel()
	outcome, err := s.Registry.AwaitCompletion(wctx, upid)
	if err != nil {
		s.logger().Warn("ttl: await destroy", "upid", string(upid), "err", err)
		return false
	}
	return outcome.Succeeded
}

// awaitFor derives the await bound for a shutdown whose PVE-side grace window is
// graceSec: the whole window plus a settle margin, floored at the configured
// default.
func (s *TTL) awaitFor(graceSec int) time.Duration {
	d := time.Duration(graceSec)*time.Second + 90*time.Second
	if base := s.awaitTimeout(); d < base {
		return base
	}
	return d
}

// beginAudit writes the fail-closed intent AS SYSTEM (actor_user_id nil +
// actor_system="system:scheduler"), targeting the guest (ADR-0018/0020).
func (s *TTL) beginAudit(ctx context.Context, job store.Job, action string) (string, error) {
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

// finishAudit finalizes an intent with the outcome + a detail carrying the ttl
// and job ids (plus any extra — e.g. the tombstone config snapshot).
func (s *TTL) finishAudit(ctx context.Context, id, outcome, ttlID, jobID string, extra map[string]any) {
	detail := map[string]any{"ttl_id": ttlID, "job_id": jobID}
	for k, v := range extra {
		detail[k] = v
	}
	b, err := json.Marshal(detail)
	if err != nil {
		b = nil
	}
	if err := s.Store.FinalizeAudit(ctx, id, outcome, b); err != nil {
		s.logger().Warn("ttl: finalize audit", "audit_id", id, "err", err)
	}
}
