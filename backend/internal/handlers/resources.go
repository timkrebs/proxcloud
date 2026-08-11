package handlers

import (
	"net/http"
	"sort"
	"strconv"
	"strings"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
)

// ListResources serves GET /api/resources?type=qemu|lxc&pool=&node=&search=:
// the "All resources" list, digested from /cluster/resources guest rows.
// search matches a case-insensitive substring of the name or the VMID.
func (d *Deps) ListResources(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	typ := q.Get("type")
	if typ != "" && typ != "qemu" && typ != "lxc" {
		httpserver.WriteError(w, &types.APIError{
			Code:    "invalid_request",
			Message: `type must be "qemu" or "lxc"`,
			Status:  http.StatusBadRequest,
		})
		return
	}
	pool := q.Get("pool")
	node := q.Get("node")
	search := strings.ToLower(strings.TrimSpace(q.Get("search")))

	rows, err := d.PVE.ClusterResources(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	out := []types.GuestSummary{}
	for _, row := range rows {
		if row.Type != "qemu" && row.Type != "lxc" {
			continue
		}
		if typ != "" && row.Type != typ {
			continue
		}
		if pool != "" && row.Pool != pool {
			continue
		}
		if node != "" && row.Node != node {
			continue
		}
		if search != "" &&
			!strings.Contains(strings.ToLower(row.Name), search) &&
			!strings.Contains(strconv.Itoa(row.VMID), search) {
			continue
		}
		g := guestSummary(row)
		// Overlay the transitional status while a Proxcloud-initiated task
		// (start/stop/delete/...) is still running against this guest.
		if d.Registry != nil {
			if transitional, upid, ok := d.Registry.ActiveFor(g.VMID); ok {
				g.Status = transitional
				g.PendingTaskUPID = upid
			}
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].VMID != out[j].VMID {
			return out[i].VMID < out[j].VMID
		}
		return out[i].ID < out[j].ID
	})

	httpserver.WriteJSON(w, http.StatusOK, out)
}

// guestSummary digests one raw guest row into the wire shape: CPU fraction
// becomes percent, PVE's joined tag string becomes a slice, status passes
// through lowercased (PVE's own vocabulary is already lowercase).
func guestSummary(row proxmox.RawResource) types.GuestSummary {
	return types.GuestSummary{
		ID:        row.ID,
		Type:      row.Type,
		VMID:      row.VMID,
		Name:      row.Name,
		Node:      row.Node,
		Status:    strings.ToLower(row.Status),
		UptimeSec: row.Uptime,
		CPUPct:    row.CPU * 100,
		Cores:     row.MaxCPU,
		MemUsed:   row.Mem,
		MemMax:    row.MaxMem,
		DiskMax:   row.MaxDisk,
		Pool:      row.Pool,
		Tags:      splitPVEList(row.Tags),
		Template:  row.Template,
	}
}
