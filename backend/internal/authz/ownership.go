package authz

import (
	"context"
	"errors"

	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// ErrNotOwned is the single verdict for every ownership miss: no row for the
// VMID, a row owned by a different tenant, or a tombstoned row. Callers map it
// to 404 (never 403) so a cross-tenant probe cannot tell "does not exist" from
// "not yours" — no existence leak (ADR-0007 IDOR rule).
var ErrNotOwned = errors.New("authz: resource not owned by tenant")

// ResolveOwnership is the one code path guarding every {vmid} the API touches.
// It returns the ownership row only when it exists, belongs to tenantID, and is
// live (status active or pending); every other case is ErrNotOwned. This is the
// single insertion point used by the ResolveScope middleware (all guest routes),
// the create clone-source check, and the explicit tasks/{upid} + deployments/{id}
// checks whose paths lack a {vmid} segment.
func ResolveOwnership(ctx context.Context, s store.OwnershipStore, vmid int, tenantID string) (*store.ResourceOwnership, error) {
	o, err := s.GetOwnershipByVMID(ctx, vmid)
	if errors.Is(err, store.ErrNotFound) {
		return nil, ErrNotOwned
	}
	if err != nil {
		return nil, err
	}
	if o.TenantID != tenantID {
		return nil, ErrNotOwned
	}
	if o.Status != "active" && o.Status != "pending" {
		return nil, ErrNotOwned
	}
	return o, nil
}
