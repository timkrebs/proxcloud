package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// snapNameRe is PVE's snapshot-name rule (config-id: alnum start, then
// alnum, dash, underscore, dot).
var snapNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,39}$`)

// ListSnapshots serves GET .../snapshots.
func (d *Deps) ListSnapshots(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	snaps, err := d.PVE.Snapshots(r.Context(), ref)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, snaps)
}

// CreateSnapshot serves POST .../snapshots → 202 TaskRef.
func (d *Deps) CreateSnapshot(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	var req types.CreateSnapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !snapNameRe.MatchString(req.Name) {
		httpserver.WriteError(w, &types.APIError{
			Code:    "invalid_request",
			Message: "snapshot name must start with a letter or digit and contain only letters, digits, . - _ (max 40 chars)",
			Status:  http.StatusBadRequest,
		})
		return
	}
	upid, err := d.PVE.CreateSnapshot(r.Context(), ref, req.Name, req.Description, req.VMState)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	label := "Create snapshot"
	d.trackRes(upid, label, "", types.TaskResource{Type: ref.Type, VMID: ref.VMID, Node: ref.Node, Name: req.Name}, activeTenantOf(r))
	httpserver.WriteJSON(w, http.StatusAccepted, types.TaskRef{UPID: string(upid), Action: label})
}

// RollbackSnapshot serves POST .../snapshots/{name}/rollback → 202.
//
// Quota gate: a rollback restores the snapshot's STORED config — rolling back
// to a snapshot with more cores/memory than the guest runs today is a grow, so
// it goes through the same advisory-locked growth reservation as a config
// change. Reservation failure → 409 quota_exceeded and NO rollback call.
func (d *Deps) RollbackSnapshot(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	name := chi.URLParam(r, "name")
	if !snapNameRe.MatchString(name) {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: fmt.Sprintf("invalid snapshot name %q", name), Status: http.StatusBadRequest})
		return
	}
	snapCfg, err := d.PVE.SnapshotConfig(r.Context(), ref, name)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// A rollback restores the snapshot's cores/sockets/memory, which may exceed
	// today's — gate it exactly like a config grow. Fail closed: sizing that
	// cannot be read refuses the rollback instead of letting it through ungated.
	vcpu, ramMB, serr := rollbackGrowthTarget(ref.Type, snapCfg)
	if errors.Is(serr, errUnlimitedCores) {
		httpserver.WriteError(w, &types.APIError{
			Code:    "conflict",
			Message: "This container snapshot sets no CPU limit, so rolling back would let the container use every host core — a size quota cannot verify. The rollback was refused.",
			Status:  http.StatusConflict,
		})
		return
	}
	if serr != nil {
		httpserver.WriteError(w, sizingUnreadable("snapshot", serr))
		return
	}
	if gerr := d.enforceGrowth(r, ref, vcpu, ramMB); gerr != nil {
		httpserver.WriteError(w, gerr)
		return
	}
	upid, err := d.PVE.RollbackSnapshot(r.Context(), ref, name)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	label := "Roll back snapshot"
	d.trackRes(upid, label, "restarting", types.TaskResource{Type: ref.Type, VMID: ref.VMID, Node: ref.Node, Name: name}, activeTenantOf(r))
	httpserver.WriteJSON(w, http.StatusAccepted, types.TaskRef{UPID: string(upid), Action: label})
}

// DeleteSnapshot serves DELETE .../snapshots/{name} → 202.
func (d *Deps) DeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	name := chi.URLParam(r, "name")
	if !snapNameRe.MatchString(name) {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: fmt.Sprintf("invalid snapshot name %q", name), Status: http.StatusBadRequest})
		return
	}
	upid, err := d.PVE.DeleteSnapshot(r.Context(), ref, name)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	label := "Delete snapshot"
	d.trackRes(upid, label, "", types.TaskResource{Type: ref.Type, VMID: ref.VMID, Node: ref.Node, Name: name}, activeTenantOf(r))
	httpserver.WriteJSON(w, http.StatusAccepted, types.TaskRef{UPID: string(upid), Action: label})
}

// GetGuestFirewall serves GET .../firewall.
func (d *Deps) GetGuestFirewall(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	fw, err := d.PVE.FirewallRules(r.Context(), ref)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, fw)
}

// SetGuestFirewall serves PUT .../firewall/options {enable}.
func (d *Deps) SetGuestFirewall(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	var req struct {
		Enable bool `json:"enable"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: "body must be JSON with enable", Status: http.StatusBadRequest})
		return
	}
	if err := d.PVE.SetFirewallEnabled(r.Context(), ref, req.Enable); err != nil {
		httpserver.WriteError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// GetGuestACL serves GET .../acl — entries whose path targets this guest
// or a parent scope (/, /vms), read-only.
func (d *Deps) GetGuestACL(w http.ResponseWriter, r *http.Request) {
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	all, err := d.PVE.ACL(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	guestPath := fmt.Sprintf("/vms/%d", ref.VMID)
	out := []types.ACLEntry{}
	for _, e := range all {
		if e.Path == guestPath || e.Path == "/" || e.Path == "/vms" {
			out = append(out, e)
		}
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}
