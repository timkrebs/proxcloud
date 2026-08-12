// Package bootstrap holds one-shot, pre-serve startup routines that reconcile
// the Postgres system of record with the live Proxmox cluster. Its first job is
// the ownership backfill (ADR-0010): before enforcement ever comes online, every
// pre-existing guest on the cluster is claimed into the default tenant/project so
// a later chunk's scoping middleware can never 404 a guest the platform already
// runs. Everything here is idempotent and best-effort against Proxmox — a
// transient PVE failure logs and is retried on the next boot (or by the Phase-4
// reconciler), never failing startup.
package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

const (
	// defaultTenantSlug and defaultProjectPool are seeded by migration 000001;
	// the backfill adopts pre-existing guests into this tenant/project.
	defaultTenantSlug  = "default"
	defaultProjectPool = "pc-default-default"
	// defaultPoolComment tags pools Proxcloud creates so they are recognizable
	// in the Proxmox UI.
	defaultPoolComment = "managed by Proxcloud"
)

// EnsureProjectPool creates a project's Proxmox pool if it is not already there.
// CreatePool is idempotent (a "pool already exists" 500 is treated as success),
// so this is a safe "ensure". Reused by the create-guest handler in a later
// chunk; here it guarantees the default pool exists before members are added.
func EnsureProjectPool(ctx context.Context, pve proxmox.Client, poolID, comment string) error {
	return pve.CreatePool(ctx, poolID, comment)
}

// BackfillOwnership claims every qemu/lxc guest on the cluster that lacks an
// ownership row into the default tenant/project, and best-effort adds it to the
// default pool. It is idempotent: guests that already have a live ownership row
// are skipped, so a second run is a no-op.
//
// It MUST run synchronously before the server starts serving (see main.go:
// RunMigrations -> SeedEnvAdmin -> construct pve -> BackfillOwnership -> serve)
// so scoping enforcement — always on once the router is up in a later chunk —
// never 404s a pre-existing guest.
//
// All Proxmox calls are best-effort: a failure to reach the cluster or add a
// pool member logs and the function still returns nil, leaving the retry to the
// next boot or the Phase-4 reconciler. It returns a non-nil error only when the
// local system of record is itself unusable (default tenant/project missing, or
// the ownership snapshot query fails) — a genuine fail-closed condition.
func BackfillOwnership(ctx context.Context, s store.Store, pve proxmox.Client, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}

	// 1. Ensure the default project's pool exists (best-effort, idempotent).
	if err := EnsureProjectPool(ctx, pve, defaultProjectPool, defaultPoolComment); err != nil {
		log.Warn("backfill: ensure default pool failed (continuing)",
			"pool", defaultProjectPool, "err", err)
	}

	// 2. Load the default tenant + project (seeded by migration 000001). A miss
	//    here is a broken system of record, not a transient PVE issue — fail.
	tenant, err := s.GetTenantBySlug(ctx, defaultTenantSlug)
	if err != nil {
		return fmt.Errorf("backfill: load default tenant %q: %w", defaultTenantSlug, err)
	}
	project, err := s.GetProjectByPoolID(ctx, defaultProjectPool)
	if err != nil {
		return fmt.Errorf("backfill: load default project (pool %q): %w", defaultProjectPool, err)
	}

	// 3. Snapshot which VMIDs already have a live ownership row (one query).
	owned, err := s.ListActiveVMIDs(ctx)
	if err != nil {
		return fmt.Errorf("backfill: list owned vmids: %w", err)
	}

	// 4. Enumerate cluster guests. A PVE hiccup here must not fail boot; the
	//    next boot (or the Phase-4 reconciler) re-attempts.
	resources, err := pve.ClusterResources(ctx)
	if err != nil {
		log.Warn("backfill: cluster resources unavailable (skipping this boot)", "err", err)
		return nil
	}

	var claimed, skipped, failed int
	for _, r := range resources {
		if r.Type != "qemu" && r.Type != "lxc" {
			continue
		}
		if owned[r.VMID] {
			skipped++
			continue
		}
		// The ownership-row write is the claim; a pool-add failure inside
		// ClaimIntoProject is best-effort (logged, not returned), so `claimed`
		// counts committed rows and does not flip to `failed` on a cosmetic
		// pool-membership hiccup.
		if err := ClaimIntoProject(ctx, s, pve, r, tenant.ID, project.ID, nil, log); err != nil {
			failed++
			log.Warn("backfill: claim guest failed (continuing)",
				"vmid", r.VMID, "type", r.Type, "node", r.Node, "err", err)
			continue
		}
		claimed++
	}
	log.Info("ownership backfill complete",
		"tenant", tenant.Slug, "project", project.Slug,
		"claimed", claimed, "skipped", skipped, "failed", failed)
	return nil
}

// ClaimIntoProject records ownership of a single guest into (tenantID,
// projectID) and best-effort adds it to that project's Proxmox pool. The
// Phase-4 reconciler reuses it to adopt unassigned guests. actor is the
// claiming user's id, or nil for a system/backfill claim (createdBy stays NULL).
//
// The ownership row is the critical, must-succeed step (it is what enforcement
// stands on); the pool membership is cosmetic grouping. A non-nil return means
// ONLY that the ownership row could not be written — that is the sole condition
// that fails a claim. Once the row is committed the claim has succeeded: a
// failure to resolve or add the pool member is logged (Warn) and swallowed, so
// the caller's success counter reflects committed rows and a transient PVE
// hiccup never re-marks a claimed guest as failed. The reconciler re-adds the
// pool membership on a later pass; the guest is meanwhile fully visible to a
// scoped view via its ownership row.
func ClaimIntoProject(ctx context.Context, s store.Store, pve proxmox.Client, row proxmox.RawResource, tenantID, projectID string, actor *string, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	if _, err := s.CreateOwnership(ctx, store.CreateOwnershipParams{
		TenantID:  tenantID,
		ProjectID: projectID,
		VMID:      row.VMID,
		GuestType: row.Type,
		Node:      row.Node,
		CreatedBy: actor,
		Status:    "active",
	}); err != nil {
		return fmt.Errorf("create ownership for vmid %d: %w", row.VMID, err)
	}

	// Ownership committed → the claim has succeeded. Everything below is
	// best-effort pool grouping: log and continue on failure, never return.
	project, err := s.GetProjectByID(ctx, projectID)
	if err != nil {
		log.Warn("backfill: resolve pool for claimed guest failed (ownership recorded; pool add skipped)",
			"vmid", row.VMID, "project", projectID, "err", err)
		return nil
	}
	if err := pve.AddPoolMembers(ctx, project.PoolID, []int{row.VMID}); err != nil {
		log.Warn("backfill: add claimed guest to pool failed (ownership recorded; retried next pass)",
			"vmid", row.VMID, "pool", project.PoolID, "err", err)
	}
	return nil
}
