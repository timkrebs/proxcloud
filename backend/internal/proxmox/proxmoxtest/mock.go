// Package proxmoxtest provides a struct-of-funcs mock of proxmox.Client for
// table-driven handler tests: set only the On<Name> fields a case needs.
package proxmoxtest

import (
	"context"
	"fmt"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	pmx "github.com/timkrebs9/proxcloud/backend/internal/proxmox"
)

// MockClient implements proxmox.Client by delegating every method to the
// corresponding On<Name> func field. A method whose field is nil panics with
// the method name — chi/httptest recover the panic into a clear test failure
// pointing at the unstubbed call instead of a silent zero value.
type MockClient struct {
	OnVersion          func(ctx context.Context) (string, error)
	OnClusterStatus    func(ctx context.Context) (*pmx.ClusterInfo, error)
	OnClusterResources func(ctx context.Context) ([]pmx.RawResource, error)
	OnNextID           func(ctx context.Context) (int, error)
	OnPools            func(ctx context.Context) ([]types.Pool, error)
	OnNodeStatus       func(ctx context.Context, node string) (*pmx.NodeStatusInfo, error)
	OnNodeRRD          func(ctx context.Context, node, timeframe string) (map[string][]types.MetricPoint, error)
	OnNodeBridges      func(ctx context.Context, node string) ([]types.Bridge, error)
	OnNodeStorages     func(ctx context.Context, node, content string) ([]types.NodeStorage, error)
	OnStorageContent   func(ctx context.Context, node, storage, content string) ([]types.StorageContentItem, error)
	OnGuestStatus      func(ctx context.Context, ref pmx.GuestRef) (*pmx.GuestStatusInfo, error)
	OnGuestAction      func(ctx context.Context, ref pmx.GuestRef, action string) (pmx.UPID, error)
	OnDeleteGuest      func(ctx context.Context, ref pmx.GuestRef, purge bool) (pmx.UPID, error)
	OnClusterTasks     func(ctx context.Context) ([]pmx.TaskInfo, error)
	OnTaskStatus       func(ctx context.Context, upid pmx.UPID) (*pmx.TaskInfo, error)
	OnTaskLog          func(ctx context.Context, upid pmx.UPID, start, limit int) ([]types.TaskLogLine, int, error)

	OnGuestConfig        func(ctx context.Context, ref pmx.GuestRef) (map[string]any, error)
	OnSetGuestConfig     func(ctx context.Context, ref pmx.GuestRef, changes map[string]any) (pmx.UPID, error)
	OnGuestRRD           func(ctx context.Context, ref pmx.GuestRef, timeframe string) (map[string][]types.MetricPoint, error)
	OnAgentInterfaces    func(ctx context.Context, ref pmx.GuestRef) ([]types.GuestNIC, error)
	OnResizeDisk         func(ctx context.Context, ref pmx.GuestRef, disk, size string) (pmx.UPID, error)
	OnSnapshots          func(ctx context.Context, ref pmx.GuestRef) ([]types.Snapshot, error)
	OnCreateSnapshot     func(ctx context.Context, ref pmx.GuestRef, name, desc string, vmstate bool) (pmx.UPID, error)
	OnRollbackSnapshot   func(ctx context.Context, ref pmx.GuestRef, name string) (pmx.UPID, error)
	OnDeleteSnapshot     func(ctx context.Context, ref pmx.GuestRef, name string) (pmx.UPID, error)
	OnFirewallRules      func(ctx context.Context, ref pmx.GuestRef) (*types.GuestFirewall, error)
	OnSetFirewallEnabled func(ctx context.Context, ref pmx.GuestRef, on bool) error
	OnACL                func(ctx context.Context) ([]types.ACLEntry, error)

	OnCreateVM   func(ctx context.Context, node string, params map[string]any) (pmx.UPID, error)
	OnCreateLXC  func(ctx context.Context, node string, params map[string]any) (pmx.UPID, error)
	OnCloneGuest func(ctx context.Context, src pmx.GuestRef, newVMID int, name, pool string, full bool, storage string) (pmx.UPID, error)

	OnCreatePool     func(ctx context.Context, poolID, comment string) error
	OnDeletePool     func(ctx context.Context, poolID string) error
	OnAddPoolMembers func(ctx context.Context, poolID string, vmids []int) error
}

func (m *MockClient) GuestStatus(ctx context.Context, ref pmx.GuestRef) (*pmx.GuestStatusInfo, error) {
	if m.OnGuestStatus == nil {
		panic(unstubbed("GuestStatus"))
	}
	return m.OnGuestStatus(ctx, ref)
}

func (m *MockClient) GuestAction(ctx context.Context, ref pmx.GuestRef, action string) (pmx.UPID, error) {
	if m.OnGuestAction == nil {
		panic(unstubbed("GuestAction"))
	}
	return m.OnGuestAction(ctx, ref, action)
}

func (m *MockClient) DeleteGuest(ctx context.Context, ref pmx.GuestRef, purge bool) (pmx.UPID, error) {
	if m.OnDeleteGuest == nil {
		panic(unstubbed("DeleteGuest"))
	}
	return m.OnDeleteGuest(ctx, ref, purge)
}

func (m *MockClient) ClusterTasks(ctx context.Context) ([]pmx.TaskInfo, error) {
	if m.OnClusterTasks == nil {
		panic(unstubbed("ClusterTasks"))
	}
	return m.OnClusterTasks(ctx)
}

func (m *MockClient) TaskStatus(ctx context.Context, upid pmx.UPID) (*pmx.TaskInfo, error) {
	if m.OnTaskStatus == nil {
		panic(unstubbed("TaskStatus"))
	}
	return m.OnTaskStatus(ctx, upid)
}

func (m *MockClient) TaskLog(ctx context.Context, upid pmx.UPID, start, limit int) ([]types.TaskLogLine, int, error) {
	if m.OnTaskLog == nil {
		panic(unstubbed("TaskLog"))
	}
	return m.OnTaskLog(ctx, upid, start, limit)
}

var _ pmx.Client = (*MockClient)(nil)

func (m *MockClient) Version(ctx context.Context) (string, error) {
	if m.OnVersion == nil {
		panic(unstubbed("Version"))
	}
	return m.OnVersion(ctx)
}

func (m *MockClient) ClusterStatus(ctx context.Context) (*pmx.ClusterInfo, error) {
	if m.OnClusterStatus == nil {
		panic(unstubbed("ClusterStatus"))
	}
	return m.OnClusterStatus(ctx)
}

func (m *MockClient) ClusterResources(ctx context.Context) ([]pmx.RawResource, error) {
	if m.OnClusterResources == nil {
		panic(unstubbed("ClusterResources"))
	}
	return m.OnClusterResources(ctx)
}

func (m *MockClient) NextID(ctx context.Context) (int, error) {
	if m.OnNextID == nil {
		panic(unstubbed("NextID"))
	}
	return m.OnNextID(ctx)
}

func (m *MockClient) Pools(ctx context.Context) ([]types.Pool, error) {
	if m.OnPools == nil {
		panic(unstubbed("Pools"))
	}
	return m.OnPools(ctx)
}

func (m *MockClient) NodeStatus(ctx context.Context, node string) (*pmx.NodeStatusInfo, error) {
	if m.OnNodeStatus == nil {
		panic(unstubbed("NodeStatus"))
	}
	return m.OnNodeStatus(ctx, node)
}

func (m *MockClient) NodeRRD(ctx context.Context, node, timeframe string) (map[string][]types.MetricPoint, error) {
	if m.OnNodeRRD == nil {
		panic(unstubbed("NodeRRD"))
	}
	return m.OnNodeRRD(ctx, node, timeframe)
}

func (m *MockClient) NodeBridges(ctx context.Context, node string) ([]types.Bridge, error) {
	if m.OnNodeBridges == nil {
		panic(unstubbed("NodeBridges"))
	}
	return m.OnNodeBridges(ctx, node)
}

func (m *MockClient) NodeStorages(ctx context.Context, node, content string) ([]types.NodeStorage, error) {
	if m.OnNodeStorages == nil {
		panic(unstubbed("NodeStorages"))
	}
	return m.OnNodeStorages(ctx, node, content)
}

func (m *MockClient) StorageContent(ctx context.Context, node, storage, content string) ([]types.StorageContentItem, error) {
	if m.OnStorageContent == nil {
		panic(unstubbed("StorageContent"))
	}
	return m.OnStorageContent(ctx, node, storage, content)
}

func unstubbed(method string) string {
	return fmt.Sprintf("proxmoxtest: MockClient.%s called but On%s is not set", method, method)
}

func (m *MockClient) GuestConfig(ctx context.Context, ref pmx.GuestRef) (map[string]any, error) {
	if m.OnGuestConfig == nil {
		panic(unstubbed("GuestConfig"))
	}
	return m.OnGuestConfig(ctx, ref)
}

func (m *MockClient) SetGuestConfig(ctx context.Context, ref pmx.GuestRef, changes map[string]any) (pmx.UPID, error) {
	if m.OnSetGuestConfig == nil {
		panic(unstubbed("SetGuestConfig"))
	}
	return m.OnSetGuestConfig(ctx, ref, changes)
}

func (m *MockClient) GuestRRD(ctx context.Context, ref pmx.GuestRef, timeframe string) (map[string][]types.MetricPoint, error) {
	if m.OnGuestRRD == nil {
		panic(unstubbed("GuestRRD"))
	}
	return m.OnGuestRRD(ctx, ref, timeframe)
}

func (m *MockClient) AgentInterfaces(ctx context.Context, ref pmx.GuestRef) ([]types.GuestNIC, error) {
	if m.OnAgentInterfaces == nil {
		panic(unstubbed("AgentInterfaces"))
	}
	return m.OnAgentInterfaces(ctx, ref)
}

func (m *MockClient) ResizeDisk(ctx context.Context, ref pmx.GuestRef, disk, size string) (pmx.UPID, error) {
	if m.OnResizeDisk == nil {
		panic(unstubbed("ResizeDisk"))
	}
	return m.OnResizeDisk(ctx, ref, disk, size)
}

func (m *MockClient) Snapshots(ctx context.Context, ref pmx.GuestRef) ([]types.Snapshot, error) {
	if m.OnSnapshots == nil {
		panic(unstubbed("Snapshots"))
	}
	return m.OnSnapshots(ctx, ref)
}

func (m *MockClient) CreateSnapshot(ctx context.Context, ref pmx.GuestRef, name, desc string, vmstate bool) (pmx.UPID, error) {
	if m.OnCreateSnapshot == nil {
		panic(unstubbed("CreateSnapshot"))
	}
	return m.OnCreateSnapshot(ctx, ref, name, desc, vmstate)
}

func (m *MockClient) RollbackSnapshot(ctx context.Context, ref pmx.GuestRef, name string) (pmx.UPID, error) {
	if m.OnRollbackSnapshot == nil {
		panic(unstubbed("RollbackSnapshot"))
	}
	return m.OnRollbackSnapshot(ctx, ref, name)
}

func (m *MockClient) DeleteSnapshot(ctx context.Context, ref pmx.GuestRef, name string) (pmx.UPID, error) {
	if m.OnDeleteSnapshot == nil {
		panic(unstubbed("DeleteSnapshot"))
	}
	return m.OnDeleteSnapshot(ctx, ref, name)
}

func (m *MockClient) FirewallRules(ctx context.Context, ref pmx.GuestRef) (*types.GuestFirewall, error) {
	if m.OnFirewallRules == nil {
		panic(unstubbed("FirewallRules"))
	}
	return m.OnFirewallRules(ctx, ref)
}

func (m *MockClient) SetFirewallEnabled(ctx context.Context, ref pmx.GuestRef, on bool) error {
	if m.OnSetFirewallEnabled == nil {
		panic(unstubbed("SetFirewallEnabled"))
	}
	return m.OnSetFirewallEnabled(ctx, ref, on)
}

func (m *MockClient) ACL(ctx context.Context) ([]types.ACLEntry, error) {
	if m.OnACL == nil {
		panic(unstubbed("ACL"))
	}
	return m.OnACL(ctx)
}

func (m *MockClient) CreateVM(ctx context.Context, node string, params map[string]any) (pmx.UPID, error) {
	if m.OnCreateVM == nil {
		panic(unstubbed("CreateVM"))
	}
	return m.OnCreateVM(ctx, node, params)
}

func (m *MockClient) CreateLXC(ctx context.Context, node string, params map[string]any) (pmx.UPID, error) {
	if m.OnCreateLXC == nil {
		panic(unstubbed("CreateLXC"))
	}
	return m.OnCreateLXC(ctx, node, params)
}

func (m *MockClient) CloneGuest(ctx context.Context, src pmx.GuestRef, newVMID int, name, pool string, full bool, storage string) (pmx.UPID, error) {
	if m.OnCloneGuest == nil {
		panic(unstubbed("CloneGuest"))
	}
	return m.OnCloneGuest(ctx, src, newVMID, name, pool, full, storage)
}

func (m *MockClient) CreatePool(ctx context.Context, poolID, comment string) error {
	if m.OnCreatePool == nil {
		panic(unstubbed("CreatePool"))
	}
	return m.OnCreatePool(ctx, poolID, comment)
}

func (m *MockClient) DeletePool(ctx context.Context, poolID string) error {
	if m.OnDeletePool == nil {
		panic(unstubbed("DeletePool"))
	}
	return m.OnDeletePool(ctx, poolID)
}

func (m *MockClient) AddPoolMembers(ctx context.Context, poolID string, vmids []int) error {
	if m.OnAddPoolMembers == nil {
		panic(unstubbed("AddPoolMembers"))
	}
	return m.OnAddPoolMembers(ctx, poolID, vmids)
}
