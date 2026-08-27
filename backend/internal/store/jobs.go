package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// jobColumns is the canonical SELECT/RETURNING projection for a Job (uuid columns
// cast to text so they scan into Go strings, matching the rest of the store).
const jobColumns = `id::text, kind, handler, tenant_id::text, project_id::text, vmid,
	payload, cron, timezone, run_at, status, attempts, max_attempts, last_error,
	locked_at, locked_by, missed_policy, created_at, updated_at`

func scanJob(row pgx.Row) (*Job, error) {
	var j Job
	err := row.Scan(&j.ID, &j.Kind, &j.Handler, &j.TenantID, &j.ProjectID, &j.VMID,
		&j.Payload, &j.Cron, &j.Timezone, &j.RunAt, &j.Status, &j.Attempts, &j.MaxAttempts,
		&j.LastError, &j.LockedAt, &j.LockedBy, &j.MissedPolicy, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// EnqueueJob implements JobStore. MaxAttempts/MissedPolicy fall back to sane
// defaults when unset so callers only specify what they mean to override.
func (s *PgStore) EnqueueJob(ctx context.Context, p EnqueueJobParams) (*Job, error) {
	maxAttempts := p.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	missed := p.MissedPolicy
	if missed == "" {
		missed = "catch_up"
	}
	const q = `INSERT INTO jobs
	             (kind, handler, tenant_id, project_id, vmid, payload, cron, timezone, run_at, max_attempts, missed_policy)
	           VALUES ($1, $2, $3::uuid, $4::uuid, $5, $6, $7, $8, $9, $10, $11)
	           RETURNING ` + jobColumns
	job, err := scanJob(s.q.QueryRow(ctx, q,
		p.Kind, p.Handler, p.TenantID, p.ProjectID, p.VMID, p.Payload, p.Cron, p.Timezone,
		p.RunAt, maxAttempts, missed))
	if err != nil {
		return nil, fmt.Errorf("store: enqueue job: %w", err)
	}
	return job, nil
}

// ClaimDueJobs implements JobStore. The claim + status flip is a single atomic
// UPDATE whose subquery uses FOR UPDATE SKIP LOCKED: a second instance ticking
// concurrently skips the rows this one locks rather than blocking or
// re-selecting them, so no job double-fires (ADR-0018). The returned rows are
// already status='running' with the claim stamped.
func (s *PgStore) ClaimDueJobs(ctx context.Context, now time.Time, limit int, lockedBy string) ([]Job, error) {
	if limit <= 0 {
		limit = 10
	}
	const q = `UPDATE jobs
	           SET status = 'running', locked_at = $1, locked_by = $2, updated_at = now()
	           WHERE id IN (
	             SELECT id FROM jobs
	             WHERE status = 'scheduled' AND run_at <= $1
	             ORDER BY run_at
	             FOR UPDATE SKIP LOCKED
	             LIMIT $3
	           )
	           RETURNING ` + jobColumns
	rows, err := s.q.Query(ctx, q, now, lockedBy, limit)
	if err != nil {
		return nil, fmt.Errorf("store: claim due jobs: %w", err)
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan claimed job: %w", err)
		}
		out = append(out, *j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: claim due jobs: %w", err)
	}
	return out, nil
}

// ReclaimStaleRunning implements JobStore: reset jobs stuck 'running' with a
// stale claim (a backend that crashed mid-handler) back to 'scheduled' for
// re-claim — the at-least-once recovery path. Attempts is left unchanged: a
// process crash is not a handler-reported failure, and the handlers are
// idempotent, so a re-run is safe. olderThan MUST exceed the longest grace
// window so an in-flight handler is never reclaimed out from under itself.
func (s *PgStore) ReclaimStaleRunning(ctx context.Context, olderThan time.Time) (int, error) {
	const q = `UPDATE jobs
	           SET status = 'scheduled', locked_at = NULL, locked_by = NULL, updated_at = now()
	           WHERE status = 'running' AND locked_at < $1`
	tag, err := s.q.Exec(ctx, q, olderThan)
	if err != nil {
		return 0, fmt.Errorf("store: reclaim stale running: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// CompleteJob implements JobStore: mark a one-shot job succeeded (terminal). The
// `status = 'running'` guard makes it a no-op if the job was cancelled while its
// handler was in flight (guest destroyed mid-run) — a cancelled job is never
// resurrected to 'succeeded'. 0 rows affected is that expected race, not an error.
func (s *PgStore) CompleteJob(ctx context.Context, id string) error {
	const q = `UPDATE jobs SET status = 'succeeded', locked_at = NULL, locked_by = NULL, updated_at = now()
	           WHERE id = $1::uuid AND status = 'running'`
	if _, err := s.q.Exec(ctx, q, id); err != nil {
		return fmt.Errorf("store: complete job: %w", err)
	}
	return nil
}

// RescheduleRecurring implements JobStore: return a claimed recurring job to
// 'scheduled' at its next cron boundary and clear the claim + retry counter (a
// successful run resets attempts). The `status = 'running'` guard prevents
// re-arming a job that was cancelled mid-run (its owner is gone); 0 rows is that
// expected race, not an error.
func (s *PgStore) RescheduleRecurring(ctx context.Context, id string, nextRunAt time.Time) error {
	const q = `UPDATE jobs
	           SET status = 'scheduled', run_at = $2, attempts = 0, last_error = NULL,
	               locked_at = NULL, locked_by = NULL, updated_at = now()
	           WHERE id = $1::uuid AND status = 'running'`
	if _, err := s.q.Exec(ctx, q, id, nextRunAt); err != nil {
		return fmt.Errorf("store: reschedule recurring: %w", err)
	}
	return nil
}

// FailJob implements JobStore: record a handler error and either reschedule for
// retry (attempts < max_attempts) or dead-letter to 'failed'. The branch is
// decided in SQL from the incremented attempts so the read-modify-write is
// atomic; deadLettered reports which branch ran. The `status = 'running'` guard
// makes a cancel-mid-run a no-op (returns false, nil) rather than resurrecting a
// cancelled job into 'scheduled'/'failed'.
func (s *PgStore) FailJob(ctx context.Context, id, lastErr string, retryAt time.Time) (bool, error) {
	const q = `UPDATE jobs
	           SET attempts   = attempts + 1,
	               last_error = $2,
	               status     = CASE WHEN attempts + 1 >= max_attempts THEN 'failed' ELSE 'scheduled' END,
	               run_at     = CASE WHEN attempts + 1 >= max_attempts THEN run_at ELSE $3 END,
	               locked_at  = NULL,
	               locked_by  = NULL,
	               updated_at = now()
	           WHERE id = $1::uuid AND status = 'running'
	           RETURNING status`
	var status string
	err := s.q.QueryRow(ctx, q, id, lastErr, retryAt).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil // raced to cancelled/terminal — no-op, not an error
	}
	if err != nil {
		return false, fmt.Errorf("store: fail job: %w", err)
	}
	return status == "failed", nil
}

// BumpScheduledRunAt implements JobStore: advance a still-scheduled job's run_at
// (the "skip next" primitive, ADR-0019). The `status = 'scheduled'` guard means a
// job that is mid-run or terminal is left untouched — a lost race is ErrNotFound,
// not a corruption. The claim + retry counters are untouched (this is not a run).
func (s *PgStore) BumpScheduledRunAt(ctx context.Context, id string, runAt time.Time) error {
	const q = `UPDATE jobs SET run_at = $2, updated_at = now()
	           WHERE id = $1::uuid AND status = 'scheduled'`
	tag, err := s.q.Exec(ctx, q, id, runAt)
	if err != nil {
		return fmt.Errorf("store: bump scheduled run_at: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// CancelJobsForVMID implements JobStore: cancel every non-terminal job owned by
// vmid — the destroy choke-point cleanup so no orphaned job acts on a gone VMID.
// Idempotent: already-terminal jobs match nothing and it returns 0.
func (s *PgStore) CancelJobsForVMID(ctx context.Context, vmid int) (int, error) {
	const q = `UPDATE jobs
	           SET status = 'cancelled', locked_at = NULL, locked_by = NULL, updated_at = now()
	           WHERE vmid = $1 AND status IN ('scheduled', 'running')`
	tag, err := s.q.Exec(ctx, q, vmid)
	if err != nil {
		return 0, fmt.Errorf("store: cancel jobs for vmid: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// CancelJobsForVMIDByPrefix implements JobStore: cancel only the vmid's
// non-terminal jobs whose handler begins with prefix (the re-materialization
// cleanup that leaves other features' jobs intact). prefix is a trusted code
// constant, not user input.
func (s *PgStore) CancelJobsForVMIDByPrefix(ctx context.Context, vmid int, prefix string) (int, error) {
	const q = `UPDATE jobs
	           SET status = 'cancelled', locked_at = NULL, locked_by = NULL, updated_at = now()
	           WHERE vmid = $1 AND status IN ('scheduled', 'running') AND handler LIKE $2`
	tag, err := s.q.Exec(ctx, q, vmid, prefix+"%")
	if err != nil {
		return 0, fmt.Errorf("store: cancel jobs for vmid by prefix: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// GetJob implements JobStore.
func (s *PgStore) GetJob(ctx context.Context, id string) (*Job, error) {
	const q = `SELECT ` + jobColumns + ` FROM jobs WHERE id = $1::uuid`
	job, err := scanJob(s.q.QueryRow(ctx, q, id))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get job: %w", err)
	}
	return job, nil
}

// ListJobs implements JobStore: the admin view, newest run_at first. The tenant
// filter is in SQL (a tenant Owner never sees another tenant's jobs); an empty
// TenantID is the platform-admin all-tenants view.
func (s *PgStore) ListJobs(ctx context.Context, f JobFilter) ([]Job, error) {
	args := []any{}
	where := "TRUE"
	if f.TenantID != "" {
		args = append(args, f.TenantID)
		where += fmt.Sprintf(" AND tenant_id = $%d::uuid", len(args))
	}
	if f.Status != "" {
		args = append(args, f.Status)
		where += fmt.Sprintf(" AND status = $%d", len(args))
	}
	if f.VMID != nil {
		args = append(args, *f.VMID)
		where += fmt.Sprintf(" AND vmid = $%d", len(args))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	args = append(args, limit)
	q := `SELECT ` + jobColumns + ` FROM jobs WHERE ` + where +
		fmt.Sprintf(` ORDER BY run_at DESC, id DESC LIMIT $%d`, len(args))
	rows, err := s.q.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	defer rows.Close()
	out := []Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan job row: %w", err)
		}
		out = append(out, *j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list jobs: %w", err)
	}
	return out, nil
}
