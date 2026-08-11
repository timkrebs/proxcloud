package handlers

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
)

// pveTaskLabels maps PVE task types to activity-log labels. Unknown types
// fall back to the raw type string — real, if less pretty.
var pveTaskLabels = map[string]string{
	"qmstart":       "Start virtual machine",
	"qmstop":        "Stop virtual machine",
	"qmshutdown":    "Shut down virtual machine",
	"qmreboot":      "Restart virtual machine",
	"qmreset":       "Reset virtual machine",
	"qmcreate":      "Create virtual machine",
	"qmdestroy":     "Delete virtual machine",
	"qmclone":       "Clone virtual machine",
	"qmconfig":      "Update virtual machine configuration",
	"qmmove":        "Move disk",
	"qmigrate":      "Migrate virtual machine",
	"qmsnapshot":    "Create snapshot",
	"qmdelsnapshot": "Delete snapshot",
	"qmrollback":    "Roll back snapshot",
	"resize":        "Resize disk",
	"vzstart":       "Start container",
	"vzstop":        "Stop container",
	"vzshutdown":    "Shut down container",
	"vzreboot":      "Restart container",
	"vzcreate":      "Create container",
	"vzdestroy":     "Delete container",
	"vzclone":       "Clone container",
	"vzsnapshot":    "Create snapshot",
	"vzdelsnapshot": "Delete snapshot",
	"vzrollback":    "Roll back snapshot",
	"vzdump":        "Back up guest",
	"vncproxy":      "Console session",
	"vncshell":      "Shell session",
	"termproxy":     "Terminal session",
	"spiceproxy":    "SPICE session",
	"imgcopy":       "Copy image",
	"imgdel":        "Delete image",
	"download":      "Download file",
	"aptupdate":     "Update package index",
	"startall":      "Start all guests",
	"stopall":       "Stop all guests",
	"srvstart":      "Start service",
	"srvstop":       "Stop service",
}

func taskLabel(pveType string) string {
	if l, ok := pveTaskLabels[pveType]; ok {
		return l
	}
	return pveType
}

// taskSummary normalizes one TaskInfo into the wire shape.
func (d *Deps) taskSummary(t proxmox.TaskInfo) types.TaskSummary {
	s := types.TaskSummary{
		UPID:      string(t.UPID),
		Node:      t.Node,
		Type:      t.Type,
		Action:    taskLabel(t.Type),
		User:      t.User,
		StartedAt: time.Unix(t.StartTime, 0).UTC(),
	}
	if tr, ok := d.registryLookup(t.UPID); ok {
		s.Action = tr.Action
		s.Resource = &tr.Resource
	} else if vmid, err := strconv.Atoi(t.ID); err == nil && vmid > 0 {
		res := &types.TaskResource{VMID: vmid, Node: t.Node}
		switch {
		case strings.HasPrefix(t.Type, "qm"):
			res.Type = "qemu"
		case strings.HasPrefix(t.Type, "vz"):
			res.Type = "lxc"
		}
		s.Resource = res
	}

	if t.EndTime == 0 {
		s.Status = "running"
	} else {
		if t.EndTime > 0 {
			end := time.Unix(t.EndTime, 0).UTC()
			s.EndedAt = &end
		}
		s.ExitStatus = t.ExitStatus
		if strings.EqualFold(t.ExitStatus, "OK") || strings.HasPrefix(strings.ToLower(t.ExitStatus), "warnings:") {
			s.Status = "succeeded"
		} else {
			s.Status = "failed"
		}
	}
	return s
}

func (d *Deps) registryLookup(upid proxmox.UPID) (*struct {
	Action   string
	Resource types.TaskResource
}, bool) {
	if d.Registry == nil {
		return nil, false
	}
	tr, ok := d.Registry.Lookup(upid)
	if !ok {
		return nil, false
	}
	return &struct {
		Action   string
		Resource types.TaskResource
	}{tr.Action, tr.Resource}, true
}

// ListTasks serves GET /api/tasks?limit=&running=&vmid=.
func (d *Deps) ListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	runningOnly := q.Get("running") == "true"
	vmidFilter := 0
	if v := q.Get("vmid"); v != "" {
		vmidFilter, _ = strconv.Atoi(v)
	}

	infos, err := d.PVE.ClusterTasks(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	out := []types.TaskSummary{}
	for _, t := range infos {
		s := d.taskSummary(t)
		if runningOnly && s.Status != "running" {
			continue
		}
		if vmidFilter > 0 && (s.Resource == nil || s.Resource.VMID != vmidFilter) {
			continue
		}
		out = append(out, s)
		if len(out) >= limit {
			break
		}
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}

// upidParam extracts and unescapes the {upid} path segment.
func upidParam(r *http.Request) (proxmox.UPID, *types.APIError) {
	raw := chi.URLParam(r, "upid")
	dec, err := url.PathUnescape(raw)
	if err != nil || !strings.HasPrefix(dec, "UPID:") {
		return "", &types.APIError{Code: "invalid_request", Message: "malformed UPID", Status: http.StatusBadRequest}
	}
	return proxmox.UPID(dec), nil
}

// GetTask serves GET /api/tasks/{upid} (UPID path-escaped).
func (d *Deps) GetTask(w http.ResponseWriter, r *http.Request) {
	upid, apiErr := upidParam(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	info, err := d.PVE.TaskStatus(r.Context(), upid)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, d.taskSummary(*info))
}

// GetTaskLog serves GET /api/tasks/{upid}/log?start=&limit=.
func (d *Deps) GetTaskLog(w http.ResponseWriter, r *http.Request) {
	upid, apiErr := upidParam(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	q := r.URL.Query()
	start, _ := strconv.Atoi(q.Get("start"))
	if start < 0 {
		start = 0
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit < 1 || limit > 500 {
		limit = 200
	}

	lines, total, err := d.PVE.TaskLog(r.Context(), upid, start, limit)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, types.TaskLog{Total: total, Lines: lines})
}
