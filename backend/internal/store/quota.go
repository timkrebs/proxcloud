package store

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"

	"github.com/jackc/pgx/v5"
)

// ErrQuotaExceeded is the verdict from ReserveOwnership when a create would push
// a scope past one of its limits. It names the tightest violated dimension so the
// handler can render the contract's 409 quota_exceeded message. Used/Requested are
// the pre-create usage and the requested delta; Used+Requested > Limit.
type ErrQuotaExceeded struct {
	Scope     string // "tenant" | "project"
	Dimension string // "vcpu" | "ram_mb" | "disk_gb" | "count"
	Limit     int64
	Used      int64
	Requested int64
}

func (e ErrQuotaExceeded) Error() string {
	return fmt.Sprintf("store: %s %s quota exceeded (used %d, limit %d, requested %d)",
		e.Scope, e.Dimension, e.Used, e.Limit, e.Requested)
}

// AdvisoryKeyTenant derives the transaction-advisory-lock key that serializes a
// tenant's quota reservation read-modify-write. A single per-tenant lock covers
// BOTH the project and tenant checks because tenant usage is the superset of
// project usage (ADR-0012 §1), closing the cross-project tenant-cap race.
func AdvisoryKeyTenant(tenantID string) int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(tenantID))
	return int64(h.Sum64())
}

const quotaColumns = `id::text, scope_type, scope_id::text, max_vcpu, max_ram_mb, max_disk_gb, max_count, created_at, updated_at`

func scanQuota(row pgx.Row) (*Quota, error) {
	var q Quota
	err := row.Scan(&q.ID, &q.ScopeType, &q.ScopeID, &q.MaxVCPU, &q.MaxRAMMB,
		&q.MaxDiskGB, &q.MaxCount, &q.CreatedAt, &q.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &q, nil
}

// GetQuota implements QuotaStore. ErrNotFound means "no stored row" — the caller
// treats it as all-unlimited, never as an error.
func (s *PgStore) GetQuota(ctx context.Context, scopeType, scopeID string) (*Quota, error) {
	const q = `SELECT ` + quotaColumns + ` FROM quotas WHERE scope_type = $1 AND scope_id = $2::uuid`
	quota, err := scanQuota(s.q.QueryRow(ctx, q, scopeType, scopeID))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get quota: %w", err)
	}
	return quota, nil
}

// UpsertQuota implements QuotaStore (INSERT … ON CONFLICT (scope_type, scope_id)
// DO UPDATE). A nil limit column clears that dimension.
func (s *PgStore) UpsertQuota(ctx context.Context, p UpsertQuotaParams) (*Quota, error) {
	const q = `INSERT INTO quotas (scope_type, scope_id, max_vcpu, max_ram_mb, max_disk_gb, max_count)
	           VALUES ($1, $2::uuid, $3, $4, $5, $6)
	           ON CONFLICT (scope_type, scope_id) DO UPDATE
	             SET max_vcpu = EXCLUDED.max_vcpu,
	                 max_ram_mb = EXCLUDED.max_ram_mb,
	                 max_disk_gb = EXCLUDED.max_disk_gb,
	                 max_count = EXCLUDED.max_count,
	                 updated_at = now()
	           RETURNING ` + quotaColumns
	quota, err := scanQuota(s.q.QueryRow(ctx, q,
		p.ScopeType, p.ScopeID, p.MaxVCPU, p.MaxRAMMB, p.MaxDiskGB, p.MaxCount))
	if err != nil {
		return nil, fmt.Errorf("store: upsert quota: %w", err)
	}
	return quota, nil
}

// ComputeUsage implements QuotaStore: one tenant-filtered SELECT of active+pending
// ownership rows, aggregated in Go against snapshot (ADR-0012 §1.3).
func (s *PgStore) ComputeUsage(ctx context.Context, tenantID string, snapshot map[int]Alloc) (QuotaUsage, map[string]QuotaUsage, error) {
	const q = `SELECT vmid, project_id::text, status, reserved_vcpu, reserved_ram_mb, reserved_disk_gb
	           FROM resource_ownership
	           WHERE tenant_id = $1::uuid AND status IN ('active', 'pending')`
	rows, err := s.q.Query(ctx, q, tenantID)
	if err != nil {
		return QuotaUsage{}, nil, fmt.Errorf("store: compute usage: %w", err)
	}
	defer rows.Close()

	byProject := map[string]QuotaUsage{}
	var tenant QuotaUsage
	for rows.Next() {
		var (
			vmid              int
			projectID, status string
			rv                *int
			rr, rd            *int64
		)
		if err := rows.Scan(&vmid, &projectID, &status, &rv, &rr, &rd); err != nil {
			return QuotaUsage{}, nil, fmt.Errorf("store: scan usage row: %w", err)
		}
		alloc, counted := usageOfRow(status, vmid, snapshot, rv, rr, rd)
		if !counted {
			continue
		}
		pu := byProject[projectID]
		addAlloc(&pu, alloc)
		byProject[projectID] = pu
		addAlloc(&tenant, alloc)
	}
	if err := rows.Err(); err != nil {
		return QuotaUsage{}, nil, fmt.Errorf("store: compute usage: %w", err)
	}
	return tenant, byProject, nil
}

// usageOfRow computes one ownership row's contribution: an active row reads the
// live snapshot (absent ⇒ not counted at all — a deleted/not-yet-visible guest);
// a pending row reads its reserved_* columns and always counts. The bool reports
// whether the row contributes to the count (and alloc).
func usageOfRow(status string, vmid int, snapshot map[int]Alloc, rv *int, rr, rd *int64) (Alloc, bool) {
	switch status {
	case "active":
		a, ok := snapshot[vmid]
		if !ok {
			return Alloc{}, false
		}
		return a, true
	case "pending":
		a := Alloc{}
		if rv != nil {
			a.VCPU = *rv
		}
		if rr != nil {
			a.RAMMB = *rr
		}
		if rd != nil {
			a.DiskGB = *rd
		}
		return a, true
	default:
		return Alloc{}, false
	}
}

// addAlloc folds one guest's allocation into a running usage total (count += 1).
func addAlloc(u *QuotaUsage, a Alloc) {
	u.VCPU += a.VCPU
	u.RAMMB += a.RAMMB
	u.DiskGB += a.DiskGB
	u.Count++
}

// checkQuota returns ErrQuotaExceeded for the first non-null dimension of q whose
// usage + delta would exceed the limit (a nil q is unlimited on every dimension).
// One guest is one unit of count.
func checkQuota(scope string, q *Quota, usage QuotaUsage, delta Alloc) error {
	if q == nil {
		return nil
	}
	if q.MaxVCPU != nil && usage.VCPU+delta.VCPU > *q.MaxVCPU {
		return ErrQuotaExceeded{Scope: scope, Dimension: "vcpu", Limit: int64(*q.MaxVCPU), Used: int64(usage.VCPU), Requested: int64(delta.VCPU)}
	}
	if q.MaxRAMMB != nil && usage.RAMMB+delta.RAMMB > *q.MaxRAMMB {
		return ErrQuotaExceeded{Scope: scope, Dimension: "ram_mb", Limit: *q.MaxRAMMB, Used: usage.RAMMB, Requested: delta.RAMMB}
	}
	if q.MaxDiskGB != nil && usage.DiskGB+delta.DiskGB > *q.MaxDiskGB {
		return ErrQuotaExceeded{Scope: scope, Dimension: "disk_gb", Limit: *q.MaxDiskGB, Used: usage.DiskGB, Requested: delta.DiskGB}
	}
	if q.MaxCount != nil && usage.Count+1 > *q.MaxCount {
		return ErrQuotaExceeded{Scope: scope, Dimension: "count", Limit: int64(*q.MaxCount), Used: int64(usage.Count), Requested: 1}
	}
	return nil
}

// ReserveOwnership implements QuotaStore: the concurrency-safe reservation. The
// snapshot is fetched by the caller BEFORE this call; inside the per-tenant lock
// only one SELECT (usage) and one INSERT run, so no PVE round-trip is held under
// the lock (ADR-0009). A rollback (quota violation or conflict) releases the lock.
func (s *PgStore) ReserveOwnership(ctx context.Context, p ReserveOwnershipParams) (*ResourceOwnership, error) {
	var out *ResourceOwnership
	err := s.WithTx(ctx, func(txs Store) error {
		if err := txs.AdvisoryLock(ctx, AdvisoryKeyTenant(p.TenantID)); err != nil {
			return err
		}
		tenantUsage, byProject, err := txs.ComputeUsage(ctx, p.TenantID, p.Snapshot)
		if err != nil {
			return err
		}
		projectQuota, err := txs.GetQuota(ctx, "project", p.ProjectID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		tenantQuota, err := txs.GetQuota(ctx, "tenant", p.TenantID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		// Project first (usually the tighter cap), then tenant.
		if err := checkQuota("project", projectQuota, byProject[p.ProjectID], p.Reserved); err != nil {
			return err
		}
		if err := checkQuota("tenant", tenantQuota, tenantUsage, p.Reserved); err != nil {
			return err
		}
		rv, rr, rd := p.Reserved.VCPU, p.Reserved.RAMMB, p.Reserved.DiskGB
		o, err := txs.CreateOwnership(ctx, CreateOwnershipParams{
			TenantID:       p.TenantID,
			ProjectID:      p.ProjectID,
			VMID:           p.VMID,
			GuestType:      p.GuestType,
			Node:           p.Node,
			CreatedBy:      p.CreatedBy,
			Status:         "pending",
			ReservedVCPU:   &rv,
			ReservedRAMMB:  &rr,
			ReservedDiskGB: &rd,
		})
		if err != nil {
			return err
		}
		out = o
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReserveOwnershipBatch implements DeploymentSetStore: the atomic multi-guest
// reservation (ADR-0029). It reserves ALL N members or none under ONE per-tenant
// advisory lock. The key subtlety is the count accumulation: checkQuota hardcodes
// `+1` on count (quota.go:180), so the batch cannot reuse it N times against the
// same base usage — it must fold each accepted member into the running usage
// before checking the next, so member k is checked against base + members 0..k-1.
// The first member that would exceed any project OR tenant dimension returns a
// single ErrQuotaExceeded and the whole transaction rolls back (no partial
// cluster). All checks happen BEFORE any Proxmox call — same discipline as the
// single-guest ReserveOwnership.
func (s *PgStore) ReserveOwnershipBatch(ctx context.Context, p ReserveOwnershipBatchParams) ([]ResourceOwnership, error) {
	var out []ResourceOwnership
	err := s.WithTx(ctx, func(txs Store) error {
		if err := txs.AdvisoryLock(ctx, AdvisoryKeyTenant(p.TenantID)); err != nil {
			return err
		}
		tenantUsage, byProject, err := txs.ComputeUsage(ctx, p.TenantID, p.Snapshot)
		if err != nil {
			return err
		}
		projectQuota, err := txs.GetQuota(ctx, "project", p.ProjectID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}
		tenantQuota, err := txs.GetQuota(ctx, "tenant", p.TenantID)
		if err != nil && !errors.Is(err, ErrNotFound) {
			return err
		}

		// Running usage, seeded from live usage and ACCUMULATED across accepted
		// members (project first — usually the tighter cap — then tenant).
		projUsage := byProject[p.ProjectID]
		tenUsage := tenantUsage
		for _, m := range p.Members {
			if err := checkQuota("project", projectQuota, projUsage, m.Reserved); err != nil {
				return err
			}
			if err := checkQuota("tenant", tenantQuota, tenUsage, m.Reserved); err != nil {
				return err
			}
			// Fold this member in so the NEXT member is checked against base +
			// members up to and including this one (closes the count/vcpu leak).
			addAlloc(&projUsage, Alloc{VCPU: m.Reserved.VCPU, RAMMB: m.Reserved.RAMMB, DiskGB: m.Reserved.DiskGB})
			addAlloc(&tenUsage, Alloc{VCPU: m.Reserved.VCPU, RAMMB: m.Reserved.RAMMB, DiskGB: m.Reserved.DiskGB})
		}

		// All members fit: insert N pending rows tagged with the set id + role.
		setID := p.SetID
		out = make([]ResourceOwnership, 0, len(p.Members))
		for _, m := range p.Members {
			rv, rr, rd := m.Reserved.VCPU, m.Reserved.RAMMB, m.Reserved.DiskGB
			role := m.Role
			o, err := txs.CreateOwnership(ctx, CreateOwnershipParams{
				TenantID:        p.TenantID,
				ProjectID:       p.ProjectID,
				VMID:            m.VMID,
				GuestType:       m.GuestType,
				Node:            m.Node,
				CreatedBy:       p.CreatedBy,
				Status:          "pending",
				ReservedVCPU:    &rv,
				ReservedRAMMB:   &rr,
				ReservedDiskGB:  &rd,
				DeploymentSetID: &setID,
				Role:            &role,
			})
			if err != nil {
				return err // e.g. a duplicate VMID → ErrConflict rolls back ALL rows
			}
			out = append(out, *o)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// InsertAuditIntent implements QuotaStore: the fail-closed intent write.
func (s *PgStore) InsertAuditIntent(ctx context.Context, a AuditIntent) (string, error) {
	const q = `INSERT INTO audit_log
	             (actor_user_id, actor_system, tenant_id, project_id, action, target_type, target_id, outcome, ip)
	           VALUES ($1::uuid, $2, $3::uuid, $4::uuid, $5, $6, $7, 'pending', $8)
	           RETURNING id::text`
	var id string
	err := s.q.QueryRow(ctx, q,
		a.ActorUserID, a.ActorSystem, a.TenantID, a.ProjectID, a.Action, a.TargetType, a.TargetID, a.IP).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("store: insert audit intent: %w", err)
	}
	return id, nil
}

// FinalizeAudit implements QuotaStore: the one-way outcome/detail finalize on the
// middleware's own intent row. No other field is mutable.
func (s *PgStore) FinalizeAudit(ctx context.Context, id, outcome string, detail []byte) error {
	const q = `UPDATE audit_log SET outcome = $2, detail = $3 WHERE id = $1::uuid`
	tag, err := s.q.Exec(ctx, q, id, outcome, detail)
	if err != nil {
		return fmt.Errorf("store: finalize audit: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

const auditColumns = `id::text, ts, actor_user_id::text, actor_system, tenant_id::text, project_id::text,
	action, target_type, target_id, outcome, ip, detail`

// ListAudit implements QuotaStore: the tenant-filtered, keyset-paginated audit
// spine (ts, id DESC). The tenant filter is in SQL (no cross-tenant leak).
func (s *PgStore) ListAudit(ctx context.Context, aq AuditQuery) ([]AuditEntry, error) {
	// Build the WHERE incrementally so optional filters stay parameterized.
	args := []any{aq.TenantID}
	where := `tenant_id = $1::uuid`
	if aq.Before != nil {
		args = append(args, *aq.Before)
		where += fmt.Sprintf(" AND ts < $%d", len(args))
	}
	if aq.ProjectID != "" {
		args = append(args, aq.ProjectID)
		where += fmt.Sprintf(" AND project_id = $%d::uuid", len(args))
	}
	if aq.Outcome != "" {
		args = append(args, aq.Outcome)
		where += fmt.Sprintf(" AND outcome = $%d", len(args))
	}
	limit := aq.Limit
	if limit <= 0 {
		limit = 50
	}
	args = append(args, limit)
	q := `SELECT ` + auditColumns + ` FROM audit_log WHERE ` + where +
		fmt.Sprintf(` ORDER BY ts DESC, id DESC LIMIT $%d`, len(args))

	rows, err := s.q.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.ActorUserID, &e.ActorSystem, &e.TenantID, &e.ProjectID,
			&e.Action, &e.TargetType, &e.TargetID, &e.Outcome, &e.IP, &e.Detail); err != nil {
			return nil, fmt.Errorf("store: scan audit row: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list audit: %w", err)
	}
	return out, nil
}
