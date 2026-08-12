package proxmox

import (
	"context"
	"net/url"
	"strconv"
	"strings"
)

// Resource pools (ADR-0008). Unlike guest lifecycle calls, the /pools mutations
// are SYNCHRONOUS: PVE answers a 2xx with an empty/null body and no UPID, so
// these methods do NOT decode a upidResult and never feed the task-polling
// path. Success is simply the absence of an error.
//
// PVE surfaces pool errors as HTTP 500 with the message in the status line
// (go-proxmox turns that into errors.New(res.Status), e.g.
// "500 pool 'pc-x' already exists"). The idempotent-ensure branches below match
// on that message text before the generic mapErr, so re-running a create/add is
// a no-op instead of a hard failure. Everything else flows through mapErr and
// keeps the verbatim PVE text in PVEMessage.
//
// The live API token needs the Pool.Allocate privilege for create/delete/add;
// granting it is an operator action on the Proxmox host. A missing privilege
// comes back as a 403 and is surfaced as a clear proxmox_permission_denied
// error by mapErr.

// CreatePool implements Client: POST /pools. Idempotent — an existing pool is
// treated as success so callers can ensure-a-pool without a pre-check.
func (g *GoPVE) CreatePool(ctx context.Context, poolID, comment string) error {
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	body := map[string]any{"poolid": poolID}
	if comment != "" {
		body["comment"] = comment
	}
	// out is nil: PVE returns no body for a pool create.
	if err := g.c.Post(ctx, "/pools", body, nil); err != nil {
		if isAlreadyExists(err) {
			return nil // idempotent ensure: the pool is already there
		}
		return mapErr("create pool "+poolID, err)
	}
	return nil
}

// DeletePool implements Client: DELETE /pools/{poolid}. Synchronous, no body.
func (g *GoPVE) DeletePool(ctx context.Context, poolID string) error {
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	if err := g.c.Delete(ctx, "/pools/"+url.PathEscape(poolID), nil); err != nil {
		return mapErr("delete pool "+poolID, err)
	}
	return nil
}

// AddPoolMembers implements Client: PUT /pools/{poolid} with vms=<csv>. PVE's
// PUT ADDS the given VMIDs to the pool (it does not replace membership), so no
// allow-move/storage options are sent — one pool per guest. Idempotent: a VMID
// already in the pool is treated as success.
func (g *GoPVE) AddPoolMembers(ctx context.Context, poolID string, vmids []int) error {
	if len(vmids) == 0 {
		return nil
	}
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	ids := make([]string, len(vmids))
	for i, v := range vmids {
		ids[i] = strconv.Itoa(v)
	}
	body := map[string]any{"vms": strings.Join(ids, ",")}
	// out is nil: PVE returns no body for a pool update.
	if err := g.c.Put(ctx, "/pools/"+url.PathEscape(poolID), body, nil); err != nil {
		if isAlreadyPoolMember(err) {
			return nil // idempotent: the guest is already in the pool
		}
		return mapErr("add members to pool "+poolID, err)
	}
	return nil
}

// isAlreadyExists reports whether a pool-create failure is the benign
// "pool already exists" (PVE HTTP 500), which we treat as success.
func isAlreadyExists(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already exists")
}

// isAlreadyPoolMember reports whether a pool-add failure is the benign
// "VM is already a pool member" (PVE HTTP 500), which we treat as success.
func isAlreadyPoolMember(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "already a pool member")
}
