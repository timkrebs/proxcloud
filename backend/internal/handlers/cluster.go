package handlers

import (
	"net/http"
	"strings"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// GetCluster serves GET /api/cluster: one card's worth of cluster-wide truth
// aggregated from /cluster/status, /cluster/resources, and /version.
func (d *Deps) GetCluster(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	info, err := d.PVE.ClusterStatus(ctx)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	rows, err := d.PVE.ClusterResources(ctx)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	version, err := d.PVE.Version(ctx)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}

	sum := types.ClusterSummary{
		Name:        info.Name,
		Quorate:     info.Quorate,
		PVEVersion:  version,
		NodesOnline: info.NodesOnline,
		NodesTotal:  info.NodesTotal,
	}

	// Guest counts by type and run state. Templates are excluded: they can
	// never run, so counting them would misstate the fleet.
	for _, row := range rows {
		switch row.Type {
		case "qemu":
			if row.Template {
				continue
			}
			sum.Guests.VMsTotal++
			if strings.ToLower(row.Status) == "running" {
				sum.Guests.VMsRunning++
			}
		case "lxc":
			if row.Template {
				continue
			}
			sum.Guests.LXCsTotal++
			if strings.ToLower(row.Status) == "running" {
				sum.Guests.LXCsRunning++
			}
		}
	}

	// CPU and memory over online node rows only — an offline node reports
	// zeros and contributes no usable capacity, so including it would skew
	// the percentages. CPU is weighted by each node's core count so a busy
	// 4-core node does not average out a 64-core one.
	var cpuWeighted float64
	var cpuCapacity int
	for _, row := range rows {
		if row.Type != "node" || strings.ToLower(row.Status) != "online" {
			continue
		}
		cpuWeighted += row.CPU * float64(row.MaxCPU)
		cpuCapacity += row.MaxCPU
		sum.Usage.MemUsed += row.Mem
		sum.Usage.MemTotal += row.MaxMem
	}
	if cpuCapacity > 0 {
		sum.Usage.CPUPct = cpuWeighted / float64(cpuCapacity) * 100
	}

	// Disk over storage rows. Shared storages appear once per node in
	// /cluster/resources but are one pool of bytes — dedupe them by storage
	// name; local storages stay distinct per node. Rows PVE marks anything
	// other than "available" carry zeros and are skipped, not fabricated.
	seen := map[string]bool{}
	for _, row := range rows {
		if row.Type != "storage" || strings.ToLower(row.Status) != "available" {
			continue
		}
		name := row.Storage
		if name == "" {
			name = row.ID
		}
		key := name
		if !row.Shared {
			key = row.Node + "/" + name
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		sum.Usage.DiskUsed += row.Disk
		sum.Usage.DiskTotal += row.MaxDisk
	}

	httpserver.WriteJSON(w, http.StatusOK, sum)
}

// GetNextID serves GET /api/cluster/nextid: the next free VMID.
func (d *Deps) GetNextID(w http.ResponseWriter, r *http.Request) {
	id, err := d.PVE.NextID(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, types.NextID{VMID: id})
}
