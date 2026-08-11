package handlers

import (
	"net/http"
	"sort"
	"strings"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// ListStorage serves GET /api/storage: every storage row from
// /cluster/resources. Per-node rows are kept separate (a shared storage
// appears once per node that mounts it) with the Shared flag set, so the UI
// can group or dedupe as it sees fit without losing per-node visibility.
func (d *Deps) ListStorage(w http.ResponseWriter, r *http.Request) {
	rows, err := d.PVE.ClusterResources(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	out := []types.StorageSummary{}
	for _, row := range rows {
		if row.Type != "storage" {
			continue
		}
		name := row.Storage
		if name == "" {
			name = row.Name
		}
		out = append(out, types.StorageSummary{
			Storage: name,
			Node:    row.Node,
			Type:    row.PluginType,
			Content: splitPVEList(row.Content),
			Active:  strings.ToLower(row.Status) == "available",
			Shared:  row.Shared,
			Used:    row.Disk,
			Total:   row.MaxDisk,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Storage != out[j].Storage {
			return out[i].Storage < out[j].Storage
		}
		return out[i].Node < out[j].Node
	})

	httpserver.WriteJSON(w, http.StatusOK, out)
}
