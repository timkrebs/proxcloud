package handlers

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
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
	return proxmox.GuestRef{Node: chi.URLParam(r, "node"), Type: typ, VMID: vmid}, nil
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

	label := spec.labelQemu
	if ref.Type == "lxc" {
		label = spec.labelLXC
	}
	d.track(upid, label, spec.transitional, ref, r)
	httpserver.WriteJSON(w, http.StatusAccepted, types.TaskRef{UPID: string(upid), Action: label})
}

// DeleteGuest serves DELETE /api/guests/{node}/{type}/{vmid}?purge=1.
// A running guest is rejected with 409 — the UI's typed-name confirmation
// only appears for stopped guests, and the API enforces the same rule.
func (d *Deps) DeleteGuest(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}

	st, err := d.PVE.GuestStatus(r.Context(), ref)
	if err != nil {
		httpserver.WriteError(w, err)
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
	httpserver.WriteJSON(w, http.StatusAccepted, types.TaskRef{UPID: string(upid), Action: label})
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
