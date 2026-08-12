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

	// GuestStatus returns live status for one guest from
	// /nodes/{node}/{type}/{vmid}/status/current.
	GuestStatus(ctx context.Context, ref GuestRef) (*GuestStatusInfo, error)

	// GuestAction submits a lifecycle action (start | stop | shutdown |
	// reboot | reset) via /status/{action} and returns the task UPID.
	GuestAction(ctx context.Context, ref GuestRef, action string) (UPID, error)

	// DeleteGuest deletes a guest; purge also removes it from backup jobs
	// and destroys unreferenced disks.
	DeleteGuest(ctx context.Context, ref GuestRef, purge bool) (UPID, error)

	// ClusterTasks returns the recent task list from /cluster/tasks.
	ClusterTasks(ctx context.Context) ([]TaskInfo, error)

	// TaskStatus returns one task's live status; the node is parsed from
	// the UPID.
	TaskStatus(ctx context.Context, upid UPID) (*TaskInfo, error)

	// TaskLog returns a page of a task's log plus the total line count.
	TaskLog(ctx context.Context, upid UPID, start, limit int) ([]types.TaskLogLine, int, error)

	// GuestConfig returns the guest's raw config map (current values).
	GuestConfig(ctx context.Context, ref GuestRef) (map[string]any, error)

	// SetGuestConfig applies config changes. qemu is async (returns a
	// UPID); lxc is synchronous (returns "").
	SetGuestConfig(ctx context.Context, ref GuestRef, changes map[string]any) (UPID, error)

	// GuestRRD returns historical guest series (cpu percent 0-100, mem
	// bytes, netin/netout/diskread/diskwrite rates).
	GuestRRD(ctx context.Context, ref GuestRef, timeframe string) (map[string][]types.MetricPoint, error)

	// AgentInterfaces returns live guest IPs; ErrAgentUnavailable when the
	// QEMU guest agent is not running.
	AgentInterfaces(ctx context.Context, ref GuestRef) ([]types.GuestNIC, error)

	// ResizeDisk grows a disk to an absolute size ("64G", PVE syntax).
	ResizeDisk(ctx context.Context, ref GuestRef, disk, size string) (UPID, error)

	// Snapshot lifecycle.
	Snapshots(ctx context.Context, ref GuestRef) ([]types.Snapshot, error)
	CreateSnapshot(ctx context.Context, ref GuestRef, name, desc string, vmstate bool) (UPID, error)
	RollbackSnapshot(ctx context.Context, ref GuestRef, name string) (UPID, error)
	DeleteSnapshot(ctx context.Context, ref GuestRef, name string) (UPID, error)

	// Guest firewall (read + enable toggle in v1).
	FirewallRules(ctx context.Context, ref GuestRef) (*types.GuestFirewall, error)
	SetFirewallEnabled(ctx context.Context, ref GuestRef, on bool) error

	// ACL returns all access-control entries (handlers filter per path).
	ACL(ctx context.Context) ([]types.ACLEntry, error)

	// CreateVM / CreateLXC submit guest creation with pre-assembled PVE
	// params (built by internal/deploy) and return the creation task.
	CreateVM(ctx context.Context, node string, params map[string]any) (UPID, error)
	CreateLXC(ctx context.Context, node string, params map[string]any) (UPID, error)

	// CloneGuest clones src into newVMID (full or linked).
	CloneGuest(ctx context.Context, src GuestRef, newVMID int, name, pool string, full bool, storage string) (UPID, error)

	// Pool management (ADR-0008). These calls are SYNCHRONOUS — PVE returns a
	// 2xx with an empty body and no UPID, so they never route through the
	// task/UPID polling path. Success is the absence of an error.

	// CreatePool creates a resource pool. It is idempotent: a "pool already
	// exists" failure (PVE reports it as HTTP 500) is treated as success, so
	// callers can "ensure" a pool without a prior existence check.
	CreatePool(ctx context.Context, poolID, comment string) error
	// DeletePool removes a resource pool. The caller must have emptied it first
	// (a project delete never orphans a guest).
	DeletePool(ctx context.Context, poolID string) error
	// AddPoolMembers adds VMIDs to a pool (PVE's PUT adds, never replaces). It
	// is idempotent: an "already a pool member" failure is treated as success.
	AddPoolMembers(ctx context.Context, poolID string, vmids []int) error
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

// ID returns the task-target id embedded in the UPID (the seventh
// colon-separated field, which is the VMID for guest tasks), or "" if the value
// is not a well-formed UPID. Format:
// UPID:node:pid:pstart:starttime:type:id:user:
func (u UPID) ID() string {
	parts := strings.Split(string(u), ":")
	if len(parts) < 7 || parts[0] != "UPID" {
		return ""
	}
	return parts[6]
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

// GuestStatusInfo is the digested /nodes/{n}/{type}/{vmid}/status/current.
// Sizes are raw bytes, CPUPct is 0-100, IO fields are lifetime counters.
type GuestStatusInfo struct {
	Status    string // PVE's own vocabulary, lowercase (running | stopped | ...)
	Name      string
	UptimeSec int64
	CPUPct    float64
	Cores     int
	MemUsed   int64
	MemMax    int64
	DiskRead  int64
	DiskWrite int64
	NetIn     int64
	NetOut    int64
	// Agent reports whether the QEMU guest agent is enabled in the config
	// (qemu only; always false for lxc).
	Agent bool
}

// TaskInfo is one Proxmox task, from /cluster/tasks or a task status call.
// EndTime 0 means the task is still running. ExitStatus is PVE's verbatim
// value ("OK" or an error message), empty while running.
type TaskInfo struct {
	UPID       UPID
	Node       string
	Type       string // qmstart, vzcreate, ...
	ID         string // task target id — the VMID for guest tasks
	User       string
	StartTime  int64 // unix seconds
	EndTime    int64 // unix seconds, 0 while running
	ExitStatus string
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
