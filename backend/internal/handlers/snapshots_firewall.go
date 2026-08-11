package handlers

import (
	"encoding/json"
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
	d.trackRes(upid, label, "", types.TaskResource{Type: ref.Type, VMID: ref.VMID, Node: ref.Node, Name: req.Name})
	httpserver.WriteJSON(w, http.StatusAccepted, types.TaskRef{UPID: string(upid), Action: label})
}

// RollbackSnapshot serves POST .../snapshots/{name}/rollback → 202.
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
	upid, err := d.PVE.RollbackSnapshot(r.Context(), ref, name)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	label := "Roll back snapshot"
	d.trackRes(upid, label, "restarting", types.TaskResource{Type: ref.Type, VMID: ref.VMID, Node: ref.Node, Name: name})
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
	d.trackRes(upid, label, "", types.TaskResource{Type: ref.Type, VMID: ref.VMID, Node: ref.Node, Name: name})
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
