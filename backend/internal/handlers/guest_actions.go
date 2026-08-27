package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// actionSpec maps a lifecycle action to its friendly label per guest type
// and the transitional status shown while the task runs.
var actionSpecs = map[string]struct {
	labelQemu, labelLXC, transitional string
	qemuOnly                          bool
}{
	"start":    {"Start virtual machine", "Start container", "starting", false},
	"stop":     {"Stop virtual machine", "Stop container", "stopping", false},
	"shutdown": {"Shut down virtual machine", "Shut down container", "stopping", false},
	"reboot":   {"Restart virtual machine", "Restart container", "restarting", false},
	"reset":    {"Reset virtual machine", "", "restarting", true},
}

// guestRef parses and validates the {node}/{type}/{vmid} path segments.
func guestRef(r *http.Request) (proxmox.GuestRef, *types.APIError) {
	typ := chi.URLParam(r, "type")
	if typ != "qemu" && typ != "lxc" {
		return proxmox.GuestRef{}, &types.APIError{Code: "not_found", Message: "guest type must be qemu or lxc", Status: http.StatusNotFound}
	}
	vmid, err := strconv.Atoi(chi.URLParam(r, "vmid"))
	if err != nil || vmid < 1 {
		return proxmox.GuestRef{}, &types.APIError{Code: "not_found", Message: "invalid VMID", Status: http.StatusNotFound}
	}
	node := chi.URLParam(r, "node")
	if !ValidPVEID(node) {
		return proxmox.GuestRef{}, &types.APIError{Code: "not_found", Message: "invalid node name", Status: http.StatusNotFound}
	}
	return proxmox.GuestRef{Node: node, Type: typ, VMID: vmid}, nil
}

// GuestAction serves POST /api/guests/{node}/{type}/{vmid}/{action}.
func (d *Deps) GuestAction(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	action := chi.URLParam(r, "action")
	spec, ok := actionSpecs[action]
	if !ok {
		httpserver.WriteError(w, &types.APIError{Code: "not_found", Message: fmt.Sprintf("unknown action %q", action), Status: http.StatusNotFound})
		return
	}
	if spec.qemuOnly && ref.Type != "qemu" {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: "reset is only available for virtual machines", Status: http.StatusBadRequest})
		return
	}

	upid, err := d.PVE.GuestAction(r.Context(), ref, action)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	// A user-initiated start clears the scheduler's auto_stopped marker, so a guest
	// a user turned back on is no longer treated as "stopped by schedule" (its
	// paired autoshutdown.start would otherwise re-own the power state). Best-effort:
	// a failure here must not fail the action the user asked for (ADR-0019).
	if action == "start" && d.Store != nil {
		if err := d.Store.SetAutoStopped(r.Context(), ref.VMID, false); err != nil && !errors.Is(err, store.ErrNotFound) {
			d.logger().Warn("clear auto_stopped on manual start", "vmid", ref.VMID, "err", err)
		}
	}

	label := spec.labelQemu
	if ref.Type == "lxc" {
		label = spec.labelLXC
	}
	d.track(upid, label, spec.transitional, ref, r)
	httpserver.WriteJSON(w, http.StatusAccepted, types.TaskRef{UPID: string(upid), Action: label})
}

// DeleteGuest serves DELETE /api/guests/{node}/{type}/{vmid}?purge=1 with
// a JSON body {"confirmName": "<guest name>"}. The typed-name confirmation
// is enforced HERE, not just in the flyout: a CSRF slip, stolen cookie, or
// mis-scripted client cannot wipe a guest without naming it.
func (d *Deps) DeleteGuest(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	var body struct {
		ConfirmName string `json:"confirmName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ConfirmName == "" {
		httpserver.WriteError(w, &types.APIError{
			Code:    "invalid_request",
			Message: "Deletion requires a JSON body with confirmName matching the guest's name.",
			Status:  http.StatusBadRequest,
		})
		return
	}

	st, err := d.PVE.GuestStatus(r.Context(), ref)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if body.ConfirmName != st.Name {
		httpserver.WriteError(w, &types.APIError{
			Code:    "invalid_request",
			Message: "Confirmation name does not match the guest's name.",
			Status:  http.StatusBadRequest,
		})
		return
	}
	if st.Status == "running" {
		httpserver.WriteError(w, &types.APIError{
			Code:    "conflict",
			Message: "Guest must be stopped before deletion.",
			Status:  http.StatusConflict,
		})
		return
	}

	purge := r.URL.Query().Get("purge") == "1"
	upid, err := d.PVE.DeleteGuest(r.Context(), ref, purge)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	label := "Delete virtual machine"
	if ref.Type == "lxc" {
		label = "Delete container"
	}
	res := types.TaskResource{Type: ref.Type, VMID: ref.VMID, Node: ref.Node, Name: st.Name}
	d.trackRes(upid, label, "deleting", res)
	// Release the ownership reservation once the destroy actually completes: a
	// successful vzdestroy/qmdestroy frees the VMID for reuse (a tombstoned row
	// reads as un-owned and is revived by the next reservation). A failed destroy
	// leaves the guest — and its ownership — intact.
	if d.Store != nil && d.Registry != nil {
		go d.tombstoneOwnershipAfterDestroy(ref.VMID, upid)
	}
	httpserver.WriteJSON(w, http.StatusAccepted, types.TaskRef{UPID: string(upid), Action: label})
}

// tombstoneOwnershipAfterDestroy waits for a delete task to finish and, on
// success, tombstones the guest's resource_ownership row so the VMID is freed
// (reusable) and its reserved quota released. A failed or timed-out destroy
// leaves the guest in place, so its ownership must stand — the reconciler stays
// the backstop for any residual drift. Runs in its own goroutine: DeleteGuest
// returns 202 and must not block on the Proxmox task.
func (d *Deps) tombstoneOwnershipAfterDestroy(vmid int, upid proxmox.UPID) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	outcome, err := d.Registry.AwaitCompletion(ctx, upid)
	if err != nil {
		d.logger().Warn("await destroy for ownership release", "vmid", vmid, "err", err)
		return
	}
	if !outcome.Succeeded {
		return // guest still exists — keep its ownership row intact
	}
	own, err := d.Store.GetOwnershipByVMID(ctx, vmid)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			d.logger().Warn("lookup ownership after destroy", "vmid", vmid, "err", err)
		}
		return
	}
	if err := d.Store.TombstoneOwnership(ctx, own.ID); err != nil {
		d.logger().Warn("tombstone ownership after destroy", "vmid", vmid, "err", err)
	}
	// Cancel the guest's scheduler jobs (auto-shutdown, ADR-0019): a user-deleted
	// guest must never leave an orphaned job that acts on a reused VMID. Best-effort;
	// the handlers' defensive owner re-read is the backstop if this is missed.
	if _, err := d.Store.CancelJobsForVMID(ctx, vmid); err != nil {
		d.logger().Warn("cancel jobs after destroy", "vmid", vmid, "err", err)
	}
}

// track registers the task and announces it on the event stream. Guest name
// is resolved best-effort from the resource list so notifications read well.
func (d *Deps) track(upid proxmox.UPID, label, transitional string, ref proxmox.GuestRef, r *http.Request) {
	res := types.TaskResource{Type: ref.Type, VMID: ref.VMID, Node: ref.Node}
	if rows, err := d.PVE.ClusterResources(r.Context()); err == nil {
		for _, row := range rows {
			if row.VMID == ref.VMID && row.Type == ref.Type {
				res.Name = row.Name
				break
			}
		}
	}
	d.trackRes(upid, label, transitional, res)
}

func (d *Deps) trackRes(upid proxmox.UPID, label, transitional string, res types.TaskResource) {
	if d.Registry != nil {
		d.Registry.Track(upid, label, transitional, res)
	}
	if d.Broker != nil {
		d.Broker.Publish(events.Event{Name: "task", Data: types.TaskEvent{
			UPID: string(upid), Action: label, Status: "running", Resource: &res,
		}})
	}
}
