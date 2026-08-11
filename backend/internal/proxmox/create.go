package proxmox

import (
	"context"
	"fmt"
	"net/url"
)

// CreateVM implements Client: POST /nodes/{node}/qemu with assembled params.
func (g *GoPVE) CreateVM(ctx context.Context, node string, params map[string]any) (UPID, error) {
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	var upid upidResult
	if err := g.c.Post(ctx, "/nodes/"+url.PathEscape(node)+"/qemu", params, &upid); err != nil {
		return "", mapErr("create virtual machine", err)
	}
	return UPID(upid), nil
}

// CreateLXC implements Client: POST /nodes/{node}/lxc with assembled params.
func (g *GoPVE) CreateLXC(ctx context.Context, node string, params map[string]any) (UPID, error) {
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	var upid upidResult
	if err := g.c.Post(ctx, "/nodes/"+url.PathEscape(node)+"/lxc", params, &upid); err != nil {
		return "", mapErr("create container", err)
	}
	return UPID(upid), nil
}

// CloneGuest implements Client: POST /nodes/{n}/{type}/{vmid}/clone.
func (g *GoPVE) CloneGuest(ctx context.Context, src GuestRef, newVMID int, name, pool string, full bool, storage string) (UPID, error) {
	ctx, cancel := mutationCtx(ctx)
	defer cancel()

	body := map[string]any{"newid": newVMID}
	if name != "" {
		body["name"] = name
	}
	if pool != "" {
		body["pool"] = pool
	}
	if full {
		body["full"] = 1
		if storage != "" {
			body["storage"] = storage
		}
	}
	var upid upidResult
	if err := g.c.Post(ctx, src.path()+"/clone", body, &upid); err != nil {
		return "", mapErr(fmt.Sprintf("clone %s/%d", src.Type, src.VMID), err)
	}
	return UPID(upid), nil
}
