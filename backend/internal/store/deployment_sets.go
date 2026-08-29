package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// deploymentSetColumns is the canonical SELECT/RETURNING projection for a
// DeploymentSet (uuid columns cast to text to scan into Go strings).
const deploymentSetColumns = `id::text, tenant_id::text, project_id::text, service_id, status, created_at, updated_at`

func scanDeploymentSet(row pgx.Row) (*DeploymentSet, error) {
	var d DeploymentSet
	err := row.Scan(&d.ID, &d.TenantID, &d.ProjectID, &d.ServiceID, &d.Status, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

// CreateDeploymentSet implements DeploymentSetStore. An empty status defaults to
// the DB default ('provisioning').
func (s *PgStore) CreateDeploymentSet(ctx context.Context, p CreateDeploymentSetParams) (*DeploymentSet, error) {
	status := p.Status
	if status == "" {
		status = "provisioning"
	}
	const q = `INSERT INTO deployment_set (tenant_id, project_id, service_id, status)
	           VALUES ($1::uuid, $2::uuid, $3, $4)
	           RETURNING ` + deploymentSetColumns
	d, err := scanDeploymentSet(s.q.QueryRow(ctx, q, p.TenantID, p.ProjectID, p.ServiceID, status))
	if err != nil {
		return nil, fmt.Errorf("store: create deployment set: %w", err)
	}
	return d, nil
}

// GetDeploymentSet implements DeploymentSetStore.
func (s *PgStore) GetDeploymentSet(ctx context.Context, id string) (*DeploymentSet, error) {
	const q = `SELECT ` + deploymentSetColumns + ` FROM deployment_set WHERE id = $1::uuid`
	d, err := scanDeploymentSet(s.q.QueryRow(ctx, q, id))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("store: get deployment set: %w", err)
	}
	return d, nil
}

// ListDeploymentSets implements DeploymentSetStore (tenant filter in SQL).
func (s *PgStore) ListDeploymentSets(ctx context.Context, tenantID string) ([]DeploymentSet, error) {
	const q = `SELECT ` + deploymentSetColumns + ` FROM deployment_set
	           WHERE tenant_id = $1::uuid ORDER BY created_at DESC, id DESC`
	rows, err := s.q.Query(ctx, q, tenantID)
	if err != nil {
		return nil, fmt.Errorf("store: list deployment sets: %w", err)
	}
	defer rows.Close()
	out := []DeploymentSet{}
	for rows.Next() {
		d, err := scanDeploymentSet(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan deployment set: %w", err)
		}
		out = append(out, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list deployment sets: %w", err)
	}
	return out, nil
}

// ListSetMembers implements DeploymentSetStore. Ordered by role DESC then vmid so
// 'server' sorts before 'agent' for start ordering; callers reverse it for
// teardown (agents before server, ADR-0030).
func (s *PgStore) ListSetMembers(ctx context.Context, setID string) ([]ResourceOwnership, error) {
	const q = `SELECT ` + ownershipColumns + ` FROM resource_ownership
	           WHERE deployment_set_id = $1::uuid ORDER BY role DESC, vmid`
	rows, err := s.q.Query(ctx, q, setID)
	if err != nil {
		return nil, fmt.Errorf("store: list set members: %w", err)
	}
	defer rows.Close()
	out := []ResourceOwnership{}
	for rows.Next() {
		o, err := scanOwnership(rows)
		if err != nil {
			return nil, fmt.Errorf("store: scan set member: %w", err)
		}
		out = append(out, *o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list set members: %w", err)
	}
	return out, nil
}

// UpdateSetStatus implements DeploymentSetStore.
func (s *PgStore) UpdateSetStatus(ctx context.Context, id, status string) error {
	const q = `UPDATE deployment_set SET status = $2, updated_at = now() WHERE id = $1::uuid`
	tag, err := s.q.Exec(ctx, q, id, status)
	if err != nil {
		return fmt.Errorf("store: update set status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteDeploymentSet implements DeploymentSetStore. The FK's ON DELETE SET NULL
// nulls each member's deployment_set_id.
func (s *PgStore) DeleteDeploymentSet(ctx context.Context, id string) error {
	const q = `DELETE FROM deployment_set WHERE id = $1::uuid`
	tag, err := s.q.Exec(ctx, q, id)
	if err != nil {
		return fmt.Errorf("store: delete deployment set: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
