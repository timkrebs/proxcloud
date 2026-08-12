package bootstrap_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/bootstrap"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// memStore is a targeted store.Store double: it embeds the interface (nil) so it
// satisfies store.Store, and implements only the handful of methods the backfill
// touches. Any other call would nil-panic — a loud signal the backfill grew a
// new store dependency the test must account for.
type memStore struct {
	store.Store

	mu          sync.Mutex
	tenant      *store.Tenant
	project     *store.Project
	ownership   map[int]*store.ResourceOwnership
	createCalls int
	seq         int
}

func newMemStore() *memStore {
	return &memStore{
		tenant:    &store.Tenant{ID: "ten-1", Slug: "default", Name: "Default"},
		project:   &store.Project{ID: "proj-1", TenantID: "ten-1", Slug: "default", Name: "Default", PoolID: "pc-default-default"},
		ownership: map[int]*store.ResourceOwnership{},
	}
}

func (m *memStore) GetTenantBySlug(_ context.Context, slug string) (*store.Tenant, error) {
	if m.tenant != nil && m.tenant.Slug == slug {
		return m.tenant, nil
	}
	return nil, store.ErrNotFound
}

func (m *memStore) GetProjectByPoolID(_ context.Context, poolID string) (*store.Project, error) {
	if m.project != nil && m.project.PoolID == poolID {
		return m.project, nil
	}
	return nil, store.ErrNotFound
}

func (m *memStore) GetProjectByID(_ context.Context, id string) (*store.Project, error) {
	if m.project != nil && m.project.ID == id {
		return m.project, nil
	}
	return nil, store.ErrNotFound
}

func (m *memStore) ListActiveVMIDs(context.Context) (map[int]bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[int]bool{}
	for vmid, o := range m.ownership {
		if o.Status == "active" || o.Status == "pending" {
			out[vmid] = true
		}
	}
	return out, nil
}

func (m *memStore) CreateOwnership(_ context.Context, p store.CreateOwnershipParams) (*store.ResourceOwnership, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.createCalls++
	if _, ok := m.ownership[p.VMID]; ok {
		return nil, fmt.Errorf("duplicate vmid %d", p.VMID)
	}
	m.seq++
	o := &store.ResourceOwnership{
		ID: fmt.Sprintf("own-%d", m.seq), TenantID: p.TenantID, ProjectID: p.ProjectID,
		VMID: p.VMID, GuestType: p.GuestType, Node: p.Node, CreatedBy: p.CreatedBy, Status: p.Status,
	}
	m.ownership[p.VMID] = o
	return o, nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func sampleResources() []proxmox.RawResource {
	return []proxmox.RawResource{
		{Type: "qemu", VMID: 101, Node: "pve01", Name: "web"},
		{Type: "lxc", VMID: 102, Node: "pve01", Name: "db"},
		{Type: "node", Node: "pve01"},
		{Type: "storage", Storage: "local", Node: "pve01"},
	}
}

func TestBackfillClaimsUnownedGuests(t *testing.T) {
	ms := newMemStore()

	var (
		mu          sync.Mutex
		createdPool string
		poolAdds    = map[int]string{} // vmid -> pool it was added to
	)
	mock := &proxmoxtest.MockClient{
		OnCreatePool: func(_ context.Context, poolID, _ string) error {
			mu.Lock()
			createdPool = poolID
			mu.Unlock()
			return nil
		},
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return sampleResources(), nil },
		OnAddPoolMembers: func(_ context.Context, poolID string, vmids []int) error {
			mu.Lock()
			for _, v := range vmids {
				poolAdds[v] = poolID
			}
			mu.Unlock()
			return nil
		},
	}

	if err := bootstrap.BackfillOwnership(context.Background(), ms, mock, discardLogger()); err != nil {
		t.Fatalf("BackfillOwnership: %v", err)
	}

	// Only the two guests are claimed; node/storage rows are ignored.
	if len(ms.ownership) != 2 {
		t.Fatalf("ownership rows = %d, want 2 (qemu+lxc only)", len(ms.ownership))
	}
	o101 := ms.ownership[101]
	if o101 == nil || o101.Status != "active" || o101.GuestType != "qemu" ||
		o101.TenantID != "ten-1" || o101.ProjectID != "proj-1" || o101.Node != "pve01" {
		t.Fatalf("vmid 101 ownership = %+v", o101)
	}
	if o101.CreatedBy != nil {
		t.Fatalf("backfilled ownership CreatedBy = %v, want nil (system claim)", *o101.CreatedBy)
	}
	if o102 := ms.ownership[102]; o102 == nil || o102.GuestType != "lxc" {
		t.Fatalf("vmid 102 ownership = %+v", o102)
	}

	if createdPool != "pc-default-default" {
		t.Fatalf("ensured pool = %q, want pc-default-default", createdPool)
	}
	if poolAdds[101] != "pc-default-default" || poolAdds[102] != "pc-default-default" {
		t.Fatalf("pool adds = %v, want both guests added to pc-default-default", poolAdds)
	}
}

func TestBackfillIsIdempotent(t *testing.T) {
	ms := newMemStore()
	mock := &proxmoxtest.MockClient{
		OnCreatePool:       func(context.Context, string, string) error { return nil },
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return sampleResources(), nil },
		OnAddPoolMembers:   func(context.Context, string, []int) error { return nil },
	}

	if err := bootstrap.BackfillOwnership(context.Background(), ms, mock, discardLogger()); err != nil {
		t.Fatalf("BackfillOwnership (run 1): %v", err)
	}
	firstCreates := ms.createCalls
	if firstCreates != 2 {
		t.Fatalf("first run CreateOwnership calls = %d, want 2", firstCreates)
	}

	// Second run: every guest is already owned, so nothing is created again.
	if err := bootstrap.BackfillOwnership(context.Background(), ms, mock, discardLogger()); err != nil {
		t.Fatalf("BackfillOwnership (run 2): %v", err)
	}
	if ms.createCalls != firstCreates {
		t.Fatalf("second run made %d more CreateOwnership calls, want 0 (idempotent)", ms.createCalls-firstCreates)
	}
	if len(ms.ownership) != 2 {
		t.Fatalf("ownership rows after two runs = %d, want 2", len(ms.ownership))
	}
}

func TestBackfillBestEffortAgainstProxmox(t *testing.T) {
	pveErr := &types.APIError{Code: "proxmox_unreachable", Message: "cannot reach Proxmox", Status: 502}

	tests := []struct {
		name          string
		mock          *proxmoxtest.MockClient
		wantOwned     int  // ownership rows after backfill
		wantErr       bool // BackfillOwnership itself returns an error
		missingTenant bool
	}{
		{
			name: "cluster resources unavailable does not fail boot",
			mock: &proxmoxtest.MockClient{
				OnCreatePool:       func(context.Context, string, string) error { return nil },
				OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return nil, pveErr },
			},
			wantOwned: 0,
			wantErr:   false,
		},
		{
			name: "pool add failure does not fail boot but ownership persists",
			mock: &proxmoxtest.MockClient{
				OnCreatePool:       func(context.Context, string, string) error { return nil },
				OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return sampleResources(), nil },
				OnAddPoolMembers:   func(context.Context, string, []int) error { return pveErr },
			},
			wantOwned: 2,
			wantErr:   false,
		},
		{
			name: "ensure-pool failure does not fail boot",
			mock: &proxmoxtest.MockClient{
				OnCreatePool:       func(context.Context, string, string) error { return errors.New("Pool.Allocate missing") },
				OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return sampleResources(), nil },
				OnAddPoolMembers:   func(context.Context, string, []int) error { return nil },
			},
			wantOwned: 2,
			wantErr:   false,
		},
		{
			name: "missing default tenant is a fail-closed error",
			mock: &proxmoxtest.MockClient{
				OnCreatePool: func(context.Context, string, string) error { return nil },
			},
			missingTenant: true,
			wantErr:       true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ms := newMemStore()
			if tc.missingTenant {
				ms.tenant = nil
			}
			err := bootstrap.BackfillOwnership(context.Background(), ms, tc.mock, discardLogger())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("BackfillOwnership = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("BackfillOwnership = %v, want nil (Proxmox failures are best-effort)", err)
			}
			if len(ms.ownership) != tc.wantOwned {
				t.Fatalf("ownership rows = %d, want %d", len(ms.ownership), tc.wantOwned)
			}
		})
	}
}

// A pool-add failure is best-effort: the ownership row still commits and the
// guest counts as CLAIMED (not FAILED). Regression guard for QA F3, where a
// failed pool-add logged claimed=0 failed=N while N rows were actually written.
func TestBackfillCountsClaimedWhenPoolAddFails(t *testing.T) {
	ms := newMemStore()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	mock := &proxmoxtest.MockClient{
		OnCreatePool:       func(context.Context, string, string) error { return nil },
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return sampleResources(), nil },
		OnAddPoolMembers: func(context.Context, string, []int) error {
			return &types.APIError{Code: "proxmox_error", Message: "pool add failed", Status: 502}
		},
	}

	if err := bootstrap.BackfillOwnership(context.Background(), ms, mock, log); err != nil {
		t.Fatalf("BackfillOwnership: %v", err)
	}
	// Both ownership rows committed despite every pool-add failing.
	if len(ms.ownership) != 2 {
		t.Fatalf("ownership rows = %d, want 2 (a pool-add failure must not undo the claim)", len(ms.ownership))
	}
	out := buf.String()
	if !strings.Contains(out, "claimed=2") || !strings.Contains(out, "failed=0") {
		t.Fatalf("backfill summary = %q, want claimed=2 failed=0 (pool-add failure logged, not counted as a failed claim)", out)
	}
}

func TestClaimIntoProjectRecordsActor(t *testing.T) {
	ms := newMemStore()
	actor := "user-42"
	mock := &proxmoxtest.MockClient{
		OnAddPoolMembers: func(context.Context, string, []int) error { return nil },
	}
	row := proxmox.RawResource{Type: "qemu", VMID: 201, Node: "pve01"}

	if err := bootstrap.ClaimIntoProject(context.Background(), ms, mock, row, "ten-1", "proj-1", &actor, discardLogger()); err != nil {
		t.Fatalf("ClaimIntoProject: %v", err)
	}
	o := ms.ownership[201]
	if o == nil || o.CreatedBy == nil || *o.CreatedBy != actor || o.Status != "active" {
		t.Fatalf("claimed ownership = %+v, want active row created by %q", o, actor)
	}
}
