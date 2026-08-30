package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// scheduleColumns is the canonical SELECT/RETURNING projection for a Schedule
// (uuid columns cast to text so they scan into Go strings, matching the rest of
// the store). days_of_week is an integer[] that pgx scans into a Go []int.
const scheduleColumns = `id::text, scope, tenant_id::text, project_id::text, vmid,
	shutdown_time, auto_start_time, days_of_week, timezone, grace_seconds, enabled, opt_out,
	created_by::text, created_at, updated_at`

func scanSchedule(row pgx.Row) (*Schedule, error) {
	var s Schedule
	err := row.Scan(&s.ID, &s.Scope, &s.TenantID, &s.ProjectID, &s.VMID,
		&s.ShutdownTime, &s.AutoStartTime, &s.DaysOfWeek, &s.Timezone, &s.GraceSeconds,
		&s.Enabled, &s.OptOut, &s.CreatedBy, &s.CreatedAt, &s.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// UpsertResourceSchedule implements ScheduleStore. The ON CONFLICT target infers
// the partial unique index schedules_resource_uidx (tenant_id, vmid) WHERE
// scope='resource', so re-saving a guest's schedule replaces it in place.
func (s *PgStore) UpsertResourceSchedule(ctx context.Context, p UpsertResourceScheduleParams) (*Schedule, error) {
	const q = `INSERT INTO schedules
	             (scope, tenant_id, project_id, vmid, shutdown_time, auto_start_time,
	              days_of_week, timezone, grace_seconds, enabled, opt_out, created_by)
	           VALUES ('resource', $1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8, $9, $10, $11::uuid)
	           ON CONFLICT (tenant_id, vmid) WHERE scope = 'resource'
	           DO UPDATE SET
	             project_id      = EXCLUDED.project_id,
	             shutdown_time   = EXCLUDED.shutdown_time,
	             auto_start_time = EXCLUDED.auto_start_time,
	             days_of_week    = EXCLUDED.days_of_week,
	             timezone        = EXCLUDED.timezone,
	             grace_seconds   = EXCLUDED.grace_seconds,
	             enabled         = EXCLUDED.enabled,
	             opt_out         = EXCLUDED.opt_out,
	             updated_at      = now()
	           RETURNING ` + scheduleColumns
	sched, err := scanSchedule(s.q.QueryRow(ctx, q,
		p.TenantID, p.ProjectID, p.VMID, p.ShutdownTime, p.AutoStartTime,
		p.DaysOfWeek, p.Timezone, p.GraceSeconds, p.Enabled, p.OptOut, p.CreatedBy))
	if err != nil {
		return nil, fmt.Errorf("store: upsert resource schedule: %w", err)
	}
	return sched, nil
}

// UpsertProjectSchedule implements ScheduleStore. The ON CONFLICT target infers
// the partial unique index schedules_project_uidx (tenant_id, project_id) WHERE
// scope='project'. A project schedule never opts out (opt_out stays false).
func (s *PgStore) UpsertProjectSchedule(ctx context.Context, p UpsertProjectScheduleParams) (*Schedule, error) {
	const q = `INSERT INTO schedules
	             (scope, tenant_id, project_id, vmid, shutdown_time, auto_start_time,
	              days_of_week, timezone, grace_seconds, enabled, opt_out, created_by)
	           VALUES ('project', $1::uuid, $2::uuid, NULL, $3, $4, $5, $6, $7, $8, false, $9::uuid)
	           ON CONFLICT (tenant_id, project_id) WHERE scope = 'project'
	           DO UPDATE SET
	             shutdown_time   = EXCLUDED.shutdown_time,
	             auto_start_time = EXCLUDED.auto_start_time,
	             days_of_week    = EXCLUDED.days_of_week,
	             timezone        = EXCLUDED.timezone,
	             grace_seconds   = EXCLUDED.grace_seconds,
	             enabled         = EXCLUDED.enabled,
	             updated_at      = now()
	           RETURNING ` + scheduleColumns
	sched, err := scanSchedule(s.q.QueryRow(ctx, q,
		p.TenantID, p.ProjectID, p.ShutdownTime, p.AutoStartTime,
		p.DaysOfWeek, p.Timezone, p.GraceSeconds, p.Enabled, p.CreatedBy))
	if err != nil {
		return nil, fmt.Errorf("store: upsert project schedule: %w", err)
	}
	return sched, nil
}

// GetResourceSchedule implements ScheduleStore. VMIDs are globally unique in the
// cluster, so the vmid predicate identifies exactly one resource row.
func (s *PgStore) GetResourceSchedule(ctx context.Context, vmid int) (*Schedule, error) {
	const q = `SELECT ` + scheduleColumns + ` FROM schedules WHERE scope = 'resource' AND vmid = $1`
	sched, err := scanSchedule(s.q.QueryRow(ctx, q, vmid))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get resource schedule: %w", err)
	}
	return sched, nil
}

// GetProjectSchedule implements ScheduleStore.
func (s *PgStore) GetProjectSchedule(ctx context.Context, tenantID, projectID string) (*Schedule, error) {
	const q = `SELECT ` + scheduleColumns + ` FROM schedules
	           WHERE scope = 'project' AND tenant_id = $1::uuid AND project_id = $2::uuid`
	sched, err := scanSchedule(s.q.QueryRow(ctx, q, tenantID, projectID))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get project schedule: %w", err)
	}
	return sched, nil
}

// ListSchedulesByProject implements ScheduleStore (project + resource rows).
func (s *PgStore) ListSchedulesByProject(ctx context.Context, projectID string) ([]Schedule, error) {
	const q = `SELECT ` + scheduleColumns + ` FROM schedules WHERE project_id = $1::uuid ORDER BY scope, vmid`
	rows, err := s.q.Query(ctx, q, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list schedules by project: %w", err)
	}
	defer rows.Close()
	out := []Schedule{}
	for rows.Next() {
		sched, err := scanSchedule(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan schedule: %w", err)
		}
		out = append(out, *sched)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list schedules by project: %w", err)
	}
	return out, nil
}

// DeleteResourceSchedule implements ScheduleStore.
func (s *PgStore) DeleteResourceSchedule(ctx context.Context, vmid int) error {
	const q = `DELETE FROM schedules WHERE scope = 'resource' AND vmid = $1`
	tag, err := s.q.Exec(ctx, q, vmid)
	if err != nil {
		return fmt.Errorf("store: delete resource schedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteProjectSchedule implements ScheduleStore.
func (s *PgStore) DeleteProjectSchedule(ctx context.Context, tenantID, projectID string) error {
	const q = `DELETE FROM schedules WHERE scope = 'project' AND tenant_id = $1::uuid AND project_id = $2::uuid`
	tag, err := s.q.Exec(ctx, q, tenantID, projectID)
	if err != nil {
		return fmt.Errorf("store: delete project schedule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
