package handlers

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// nodeStatusTimeout caps each per-node /status call in the node list fan-in
// so one wedged node cannot stall the whole listing.
const nodeStatusTimeout = 5 * time.Second

// ListNodes serves GET /api/nodes: node rows from /cluster/resources,
// enriched concurrently with live /nodes/{node}/status detail. A node whose
// status call fails (or that the cluster already reports offline) becomes an
// honest Online:false row with zero values — numbers are never fabricated.
func (d *Deps) ListNodes(w http.ResponseWriter, r *http.Request) {
	rows, err := d.PVE.ClusterResources(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	type nodeRow struct {
		name   string
		online bool
	}
	nodes := []nodeRow{}
	for _, row := range rows {
		if row.Type != "node" {
			continue
		}
		nodes = append(nodes, nodeRow{name: row.Node, online: strings.ToLower(row.Status) == "online"})
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].name < nodes[j].name })

	out := make([]types.NodeSummary, len(nodes))
	var wg sync.WaitGroup
	for i, n := range nodes {
		if !n.online {
			// The cluster already knows this node is down; probing it would
			// only burn the timeout to learn the same thing.
			out[i] = offlineNodeRow(n.name)
			continue
		}
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(r.Context(), nodeStatusTimeout)
			defer cancel()
			st, err := d.PVE.NodeStatus(ctx, name)
			if err != nil {
				d.logger().Warn("node status failed", "node", name, "err", err)
				out[i] = offlineNodeRow(name)
				return
			}
			out[i] = types.NodeSummary{
				Node:       name,
				Online:     true,
				CPUPct:     st.CPUPct,
				MemUsed:    st.MemUsed,
				MemTotal:   st.MemTotal,
				DiskUsed:   st.RootFSUsed,
				DiskTotal:  st.RootFSTotal,
				UptimeSec:  st.Uptime,
				PVEVersion: st.PVEVersion,
				LoadAvg:    nonNilFloats(st.LoadAvg),
			}
		}(i, n.name)
	}
	wg.Wait()

	httpserver.WriteJSON(w, http.StatusOK, out)
}

// offlineNodeRow is the honest shape for a node we cannot read: identity,
// Online=false, and zeros — never stale or invented numbers.
func offlineNodeRow(name string) types.NodeSummary {
	return types.NodeSummary{Node: name, Online: false, LoadAvg: []float64{}}
}

// GetNode serves GET /api/nodes/{node}: live detail for one node.
func (d *Deps) GetNode(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	if !ValidPVEID(node) {
		httpserver.WriteError(w, &types.APIError{Code: "not_found", Message: "invalid node name", Status: http.StatusNotFound})
		return
	}
	st, err := d.PVE.NodeStatus(r.Context(), node)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, types.NodeDetail{
		Node:       node,
		Online:     true, // the node answered its own /status
		CPUPct:     st.CPUPct,
		MemUsed:    st.MemUsed,
		MemTotal:   st.MemTotal,
		DiskUsed:   st.RootFSUsed,
		DiskTotal:  st.RootFSTotal,
		UptimeSec:  st.Uptime,
		PVEVersion: st.PVEVersion,
		LoadAvg:    nonNilFloats(st.LoadAvg),

		KernelVersion: st.KernelVersion,
		CPUModel:      st.CPUModel,
		CPUCores:      st.CPUCores,
		CPUSockets:    st.CPUSockets,
		SwapUsed:      st.SwapUsed,
		SwapTotal:     st.SwapTotal,
	})
}

// GetNodeMetrics serves GET /api/nodes/{node}/metrics?timeframe=hour|day|week.
// The client validates the timeframe (400 on garbage) and drops null RRD
// samples instead of fabricating zeros.
func (d *Deps) GetNodeMetrics(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	if !ValidPVEID(node) {
		httpserver.WriteError(w, &types.APIError{Code: "not_found", Message: "invalid node name", Status: http.StatusNotFound})
		return
	}
	timeframe := r.URL.Query().Get("timeframe")
	if timeframe == "" {
		timeframe = "hour"
	}
	series, err := d.PVE.NodeRRD(r.Context(), node, timeframe)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, types.MetricsResponse{Timeframe: timeframe, Series: series})
}

// GetNodeBridges serves GET /api/nodes/{node}/bridges.
func (d *Deps) GetNodeBridges(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	if !ValidPVEID(node) {
		httpserver.WriteError(w, &types.APIError{Code: "not_found", Message: "invalid node name", Status: http.StatusNotFound})
		return
	}
	bridges, err := d.PVE.NodeBridges(r.Context(), node)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if bridges == nil {
		bridges = []types.Bridge{}
	}
	httpserver.WriteJSON(w, http.StatusOK, bridges)
}

// GetNodeStorages serves GET /api/nodes/{node}/storages?content=...
func (d *Deps) GetNodeStorages(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	if !ValidPVEID(node) {
		httpserver.WriteError(w, &types.APIError{Code: "not_found", Message: "invalid node name", Status: http.StatusNotFound})
		return
	}
	storages, err := d.PVE.NodeStorages(r.Context(), node, r.URL.Query().Get("content"))
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if storages == nil {
		storages = []types.NodeStorage{}
	}
	httpserver.WriteJSON(w, http.StatusOK, storages)
}

// GetStorageContent serves
// GET /api/nodes/{node}/storages/{storage}/content?content=iso|vztmpl.
func (d *Deps) GetStorageContent(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	storage := chi.URLParam(r, "storage")
	if !ValidPVEID(node) || !ValidPVEID(storage) {
		httpserver.WriteError(w, &types.APIError{Code: "not_found", Message: "invalid node or storage name", Status: http.StatusNotFound})
		return
	}
	items, err := d.PVE.StorageContent(r.Context(),
		node, storage, r.URL.Query().Get("content"))
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if items == nil {
		items = []types.StorageContentItem{}
	}
	httpserver.WriteJSON(w, http.StatusOK, items)
}

// catalogContentTypes are the only storage content types a tenant may enumerate
// through the create-wizard catalog: placement templates/images. Enumerating
// images/backup/rootdir on shared storage would reveal OTHER tenants' VM disks
// and backup filenames (iron rule #1 — no cross-tenant read), so the tenant
// path is restricted to these; the unrestricted GetStorageContent stays behind
// the platform-admin /api/admin surface.
var catalogContentTypes = map[string]bool{"iso": true, "vztmpl": true}

// GetStorageContentCatalog is the tenant-facing storage-content route. Same as
// GetStorageContent but it rejects any `content` filter outside the placement
// allowlist, so a Contributor cannot enumerate cross-tenant volumes on shared
// storage.
func (d *Deps) GetStorageContentCatalog(w http.ResponseWriter, r *http.Request) {
	if c := r.URL.Query().Get("content"); !catalogContentTypes[c] {
		httpserver.WriteError(w, &types.APIError{
			Code:    "invalid_request",
			Message: "content must be one of: iso, vztmpl",
			Status:  http.StatusBadRequest,
		})
		return
	}
	d.GetStorageContent(w, r)
}

// nonNilFloats keeps empty series as [] instead of JSON null.
func nonNilFloats(v []float64) []float64 {
	if v == nil {
		return []float64{}
	}
	return v
}
