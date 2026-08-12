// Package reconciler runs Proxcloud's background sweeps against the Postgres
// system of record. Phase-4 scope is the stale-pending reservation reclaim ONLY:
// a create reserves a pending resource_ownership row (which counts toward quota)
// before talking to Proxmox, so a backend that dies mid-create leaks that quota
// forever. This loop reclaims any pending row older than the reservation TTL and
// records one audit row for the reclaim. Everything else (PVE↔DB drift, tombstones,
// the Unassigned view) is deliberately deferred to Phase 6 (plan §2.3).
package reconciler

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// sweepTimeout bounds one full sweep's DB work so a slow database can never wedge
// the loop past the next tick.
const sweepTimeout = 60 * time.Second

// Reconciler periodically reclaims stale pending reservations. Now is injectable
// for deterministic tests; it defaults to time.Now.
type Reconciler struct {
	Store    store.Store
	Log      *slog.Logger
	Interval time.Duration
	TTL      time.Duration
	Now      func() time.Time
}

func (rc *Reconciler) logger() *slog.Logger {
	if rc.Log != nil {
		return rc.Log
	}
	return slog.Default()
}

func (rc *Reconciler) now() time.Time {
	if rc.Now != nil {
		return rc.Now()
	}
	return time.Now()
}

// Run sweeps once immediately, then every Interval until ctx is cancelled
// (graceful shutdown shares the server's context). A non-positive Interval
// disables the loop (logged) so a misconfiguration fails visible, not silent.
func (rc *Reconciler) Run(ctx context.Context) {
	if rc.Interval <= 0 {
		rc.logger().Warn("reconciler disabled (non-positive interval)")
		return
	}
	rc.logger().Info("reconciler started", "interval", rc.Interval.String(), "reservation_ttl", rc.TTL.String())
	rc.Sweep(ctx)
	ticker := time.NewTicker(rc.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			rc.logger().Info("reconciler stopped")
			return
		case <-ticker.C:
			rc.Sweep(ctx)
		}
	}
}

// Sweep reclaims every pending reservation older than TTL and returns the count
// reclaimed. Exposed (not just Run) so tests drive a single deterministic tick.
func (rc *Reconciler) Sweep(ctx context.Context) int {
	if rc.Store == nil {
		return 0
	}
	sctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()

	cutoff := rc.now().Add(-rc.TTL)
	stale, err := rc.Store.ListStalePendingOwnership(sctx, cutoff)
	if err != nil {
		rc.logger().Error("reconciler: list stale pending reservations", "err", err)
		return 0
	}

	reclaimed := 0
	for _, o := range stale {
		if err := rc.reclaim(sctx, o); err != nil {
			rc.logger().Error("reconciler: reclaim reservation",
				"ownership", o.ID, "vmid", o.VMID, "err", err)
			continue
		}
		reclaimed++
	}
	if reclaimed > 0 {
		rc.logger().Info("reconciler: reclaimed stale reservations", "count", reclaimed)
	}
	return reclaimed
}

// reclaim frees one stale reservation and records exactly one audit row for it.
// It mirrors the choke-point's fail-closed order: write the intent FIRST, so a
// reservation is never released without an audit trail; release; then finalize.
// A failed intent leaves the reservation in place (retried next tick) rather than
// freeing quota with no record.
func (rc *Reconciler) reclaim(ctx context.Context, o store.ResourceOwnership) error {
	tenant, project := o.TenantID, o.ProjectID
	targetType, targetID := "guest", strconv.Itoa(o.VMID)
	auditID, err := rc.Store.InsertAuditIntent(ctx, store.AuditIntent{
		ActorUserID: nil, // system actor — the reconciler, not a user
		TenantID:    &tenant,
		ProjectID:   &project,
		Action:      "reservation.reclaimed",
		TargetType:  &targetType,
		TargetID:    &targetID,
	})
	if err != nil {
		return err
	}
	if err := rc.Store.ReleaseOwnership(ctx, o.ID); err != nil {
		// The reservation could not be freed: record the attempt as an error so the
		// intent row is not left dangling as "pending".
		detail := []byte(`{"reason":"stale_reservation","released":false}`)
		if ferr := rc.Store.FinalizeAudit(ctx, auditID, "error", detail); ferr != nil {
			rc.logger().Error("reconciler: finalize audit after failed release", "audit_id", auditID, "err", ferr)
		}
		return err
	}
	detail := []byte(`{"reason":"stale_reservation","released":true}`)
	if err := rc.Store.FinalizeAudit(ctx, auditID, "success", detail); err != nil {
		// The release succeeded and the intent row is durable — log, do not fail.
		rc.logger().Error("reconciler: finalize reclaim audit (intent row durable)", "audit_id", auditID, "err", err)
	}
	return nil
}
