// Package proxmox is Proxcloud's single seam to the Proxmox VE API: a small
// Client interface in domain types, the production implementation over
// github.com/luthermonson/go-proxmox (gopve.go), and the error mapper that
// turns every failure into the stable *types.APIError taxonomy (errors.go).
// Handlers depend only on the interface; tests use proxmoxtest.MockClient.
package proxmox

import (
	"context"
	"strings"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

// Client is everything Proxcloud needs from Proxmox VE. Grown per milestone —
// this is the only place methods are added. Implementations own their context
// timeouts (reads 10s, mutations 30s) and return *types.APIError for every
// failure, with the verbatim PVE message in PVEMessage.
type Client interface {
	// Version returns the PVE version string (e.g. "8.2.4") from /version.
	Version(ctx context.Context) (string, error)

	// ClusterStatus returns cluster name, quorum state, and node counts
	// from /cluster/status.
	ClusterStatus(ctx context.Context) (*ClusterInfo, error)

	// ClusterResources returns every vm/lxc/storage/node row from the single
	// /cluster/resources call.
	ClusterResources(ctx context.Context) ([]RawResource, error)

	// NextID returns the next free VMID from /cluster/nextid.
	NextID(ctx context.Context) (int, error)

	// Pools lists resource pools with member counts.
	Pools(ctx context.Context) ([]types.Pool, error)

	// NodeStatus returns live detail for one node from /nodes/{node}/status.
	NodeStatus(ctx context.Context, node string) (*NodeStatusInfo, error)

	// NodeRRD returns the node's historical series from /nodes/{node}/rrddata
	// for the given timeframe (hour | day | week | month | year; "" = hour).
	// Keys: cpu, iowait (percent 0-100), memused, memtotal (bytes),
	// netin, netout (bytes/s). Null samples are dropped, never fabricated.
	NodeRRD(ctx context.Context, node, timeframe string) (map[string][]types.MetricPoint, error)

	// NodeBridges lists the node's network bridges (incl. OVS) for NIC attach.
	NodeBridges(ctx context.Context, node string) ([]types.Bridge, error)

	// NodeStorages lists storages visible on the node, optionally filtered by
	// content type (e.g. "iso", "images"; "" = all).
	NodeStorages(ctx context.Context, node, content string) ([]types.NodeStorage, error)

	// StorageContent lists volumes on one storage, optionally filtered by
	// content type (e.g. "iso", "vztmpl"; "" = all).
	StorageContent(ctx context.Context, node, storage, content string) ([]types.StorageContentItem, error)
}

// GuestRef identifies one guest for node-scoped API calls.
type GuestRef struct {
	Node string
	Type string // qemu | lxc
	VMID int
}

// UPID is a Proxmox task identifier, e.g.
// "UPID:pve01:0004C9B2:03A462AE:66B0F2E1:qmcreate:101:root@pam!proxcloud:".
type UPID string

// Node returns the node name embedded in the UPID (second colon-separated
// field), or "" if the value is not a well-formed UPID.
func (u UPID) Node() string {
	parts := strings.SplitN(string(u), ":", 3)
	if len(parts) < 3 || parts[0] != "UPID" || parts[1] == "" {
		return ""
	}
	return parts[1]
}

// ClusterInfo is the digested /cluster/status: identity, quorum, node counts.
// A standalone (non-clustered) node has Name "" and counts as quorate while
// it is online — a single node is trivially its own quorum.
type ClusterInfo struct {
	Name        string
	Quorate     bool
	NodesOnline int
	NodesTotal  int
}

// RawResource is one row of /cluster/resources, lightly typed but not
// interpreted: CPU stays a 0-1 fraction, Tags stays PVE's semicolon-separated
// string, Status stays PVE's own vocabulary. Handlers digest rows into wire
// types. Which fields are set depends on Type (node | qemu | lxc | storage).
type RawResource struct {
	ID     string // e.g. "qemu/101", "node/pve01", "storage/pve01/local"
	Type   string // node | qemu | lxc | storage | pool | sdn
	Node   string
	Name   string
	Status string
	Pool   string
	Tags   string // semicolon-separated, verbatim from PVE

	VMID     int
	Template bool
	CPU      float64 // fraction 0-1
	MaxCPU   int
	Mem      int64 // bytes
	MaxMem   int64 // bytes
	Disk     int64 // bytes
	MaxDisk  int64 // bytes
	Uptime   int64 // seconds

	Storage    string
	PluginType string
	Content    string // comma-separated content types (storage rows)
	Shared     bool
	HAState    string
}

// NodeStatusInfo is the digested /nodes/{node}/status. Sizes are raw bytes;
// CPUPct is 0-100.
type NodeStatusInfo struct {
	Uptime        int64 // seconds
	CPUPct        float64
	LoadAvg       []float64
	KernelVersion string
	CPUModel      string
	CPUCores      int
	CPUSockets    int
	MemUsed       int64
	MemTotal      int64
	SwapUsed      int64
	SwapTotal     int64
	RootFSUsed    int64
	RootFSTotal   int64
	PVEVersion    string
}
