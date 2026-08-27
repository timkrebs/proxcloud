package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ttlColumns is the canonical projection for a TTL. original_duration is stored
// as bigint seconds (see migration 000007) and converted to time.Duration on scan.
const ttlColumns = `id::text, tenant_id::text, project_id::text, vmid, expires_at, action,
	warned_24h, warned_1h, original_duration, created_by::text, created_at, updated_at`

func scanTTL(row pgx.Row) (*TTL, error) {
	var (
		t       TTL
		durSecs int64
	)
	err := row.Scan(&t.ID, &t.TenantID, &t.ProjectID, &t.VMID, &t.ExpiresAt, &t.Action,
		&t.Warned24h, &t.Warned1h, &durSecs, &t.CreatedBy, &t.CreatedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t.OriginalDuration = time.Duration(durSecs) * time.Second
	return &t, nil
}

// UpsertTTL implements TTLStore. ON CONFLICT (vmid) it replaces the TTL and, by
// re-inserting, resets the warning flags to their column defaults (false) — a
// re-set TTL always starts un-warned.
func (s *PgStore) UpsertTTL(ctx context.Context, p UpsertTTLParams) (*TTL, error) {
	const q = `INSERT INTO ttls
	             (tenant_id, project_id, vmid, expires_at, action, original_duration, created_by)
	           VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::uuid)
	           ON CONFLICT (vmid) DO UPDATE SET
	             tenant_id         = EXCLUDED.tenant_id,
	             project_id        = EXCLUDED.project_id,
	             expires_at        = EXCLUDED.expires_at,
	             action            = EXCLUDED.action,
	             original_duration = EXCLUDED.original_duration,
	             created_by        = EXCLUDED.created_by,
	             warned_24h        = false,
	             warned_1h         = false,
	             updated_at        = now()
	           RETURNING ` + ttlColumns
	ttl, err := scanTTL(s.q.QueryRow(ctx, q,
		p.TenantID, p.ProjectID, p.VMID, p.ExpiresAt, p.Action,
		int64(p.OriginalDuration.Seconds()), p.CreatedBy))
	if err != nil {
		return nil, fmt.Errorf("store: upsert ttl: %w", err)
	}
	return ttl, nil
}

// GetTTL implements TTLStore.
func (s *PgStore) GetTTL(ctx context.Context, vmid int) (*TTL, error) {
	const q = `SELECT ` + ttlColumns + ` FROM ttls WHERE vmid = $1`
	ttl, err := scanTTL(s.q.QueryRow(ctx, q, vmid))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get ttl: %w", err)
	}
	return ttl, nil
}

// DeleteTTL implements TTLStore.
func (s *PgStore) DeleteTTL(ctx context.Context, vmid int) error {
	tag, err := s.q.Exec(ctx, `DELETE FROM ttls WHERE vmid = $1`, vmid)
	if err != nil {
		return fmt.Errorf("store: delete ttl: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetTTLWarned implements TTLStore: mark one warning sent (idempotent double-send
// guard). which is "24h" or "1h".
func (s *PgStore) SetTTLWarned(ctx context.Context, vmid int, which string) error {
	col := "warned_24h"
	if which == "1h" {
		col = "warned_1h"
	}
	// col is a trusted internal literal (not user input), chosen above.
	q := `UPDATE ttls SET ` + col + ` = true, updated_at = now() WHERE vmid = $1`
	tag, err := s.q.Exec(ctx, q, vmid)
	if err != nil {
		return fmt.Errorf("store: set ttl warned: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateTTLExpiry implements TTLStore: set a new expires_at and RESET both warning
// flags (the extend path re-arms the warnings for the later expiry).
func (s *PgStore) UpdateTTLExpiry(ctx context.Context, vmid int, expiresAt time.Time) error {
	const q = `UPDATE ttls
	           SET expires_at = $2, warned_24h = false, warned_1h = false, updated_at = now()
	           WHERE vmid = $1`
	tag, err := s.q.Exec(ctx, q, vmid, expiresAt)
	if err != nil {
		return fmt.Errorf("store: update ttl expiry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListTTLsByProject implements TTLStore: a project's TTLs ordered by expiry.
func (s *PgStore) ListTTLsByProject(ctx context.Context, projectID string) ([]TTL, error) {
	const q = `SELECT ` + ttlColumns + ` FROM ttls WHERE project_id = $1::uuid ORDER BY expires_at`
	rows, err := s.q.Query(ctx, q, projectID)
	if err != nil {
		return nil, fmt.Errorf("store: list ttls by project: %w", err)
	}
	defer rows.Close()
	out := []TTL{}
	for rows.Next() {
		t, err := scanTTL(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan ttl row: %w", err)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list ttls by project: %w", err)
	}
	return out, nil
}

const ttlPolicyColumns = `tenant_id::text, project_id::text, default_ttl, max_ttl, created_at, updated_at`

func scanTTLPolicy(row pgx.Row) (*ProjectTTLPolicy, error) {
	var (
		p       ProjectTTLPolicy
		defSecs *int64
		maxSecs int64
	)
	err := row.Scan(&p.TenantID, &p.ProjectID, &defSecs, &maxSecs, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if defSecs != nil {
		d := time.Duration(*defSecs) * time.Second
		p.DefaultTTL = &d
	}
	p.MaxTTL = time.Duration(maxSecs) * time.Second
	return &p, nil
}

// GetProjectTTLPolicy implements TTLStore. ErrNotFound means "no stored policy" —
// the caller treats it as the default (no default TTL, max 30 days).
func (s *PgStore) GetProjectTTLPolicy(ctx context.Context, tenantID, projectID string) (*ProjectTTLPolicy, error) {
	const q = `SELECT ` + ttlPolicyColumns + ` FROM project_ttl_policy
	           WHERE tenant_id = $1::uuid AND project_id = $2::uuid`
	pol, err := scanTTLPolicy(s.q.QueryRow(ctx, q, tenantID, projectID))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get project ttl policy: %w", err)
	}
	return pol, nil
}

// UpsertProjectTTLPolicy implements TTLStore.
func (s *PgStore) UpsertProjectTTLPolicy(ctx context.Context, p UpsertProjectTTLPolicyParams) (*ProjectTTLPolicy, error) {
	var defSecs *int64
	if p.DefaultTTL != nil {
		v := int64(p.DefaultTTL.Seconds())
		defSecs = &v
	}
	const q = `INSERT INTO project_ttl_policy (tenant_id, project_id, default_ttl, max_ttl)
	           VALUES ($1::uuid, $2::uuid, $3, $4)
	           ON CONFLICT (tenant_id, project_id) DO UPDATE SET
	             default_ttl = EXCLUDED.default_ttl,
	             max_ttl     = EXCLUDED.max_ttl,
	             updated_at  = now()
	           RETURNING ` + ttlPolicyColumns
	pol, err := scanTTLPolicy(s.q.QueryRow(ctx, q, p.TenantID, p.ProjectID, defSecs, int64(p.MaxTTL.Seconds())))
	if err != nil {
		return nil, fmt.Errorf("store: upsert project ttl policy: %w", err)
	}
	return pol, nil
}
