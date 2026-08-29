package deploy

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

// orderedWriter is a deploy.SnippetWriter that records write ORDER (the map-based
// fakeSnippetWriter in engine_test.go does not), so a set test can assert the
// server snippet is written before any agent snippet (ADR-0030 sequencing).
type orderedWriter struct {
	mu       sync.Mutex
	names    []string
	contents map[string]string
}

func (w *orderedWriter) WriteSnippet(_ context.Context, name, content string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.names = append(w.names, name)
	if w.contents == nil {
		w.contents = map[string]string{}
	}
	w.contents[name] = content
	return nil
}
func (w *orderedWriter) RemoveSnippet(context.Context, string) error { return nil }
func (w *orderedWriter) order() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.names...)
}

// setMemberReq builds a valid catalog-shaped qemu create for a set member.
func setMemberReq(vmid int, name string, ip *types.IPConfig) *types.CreateGuestRequest {
	return &types.CreateGuestRequest{
		Type: "qemu", Name: name, Node: "pve01", VMID: vmid, ProjectId: "proj-1",
		Source: types.CreateSource{Mode: "image", ImageVolID: "local:iso/proxcloud-ubuntu-24.04.img"},
		Cores:  2, MemoryMB: 2048, DiskGB: 20, Storage: "local-lvm", Bridge: "vmbr0",
		IPConfig: ip, StartAfterCreate: true,
		Catalog: &types.CatalogProvision{
			ServiceID:  "k3s-cluster",
			SnippetRef: fmt.Sprintf("local:snippets/proxcloud-%d-k3s-cluster.yaml", vmid),
		},
	}
}

// completeTracked completes upid once it is tracked (best-effort, no t.Fatalf so it
// is safe to call from a background goroutine — unlike waitTracked).
func completeTracked(reg *tasks.Registry, upid proxmox.UPID, ok bool) {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, u := range reg.Running() {
			if u == upid {
				reg.Complete(upid, ok, "OK")
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestSubmitSetOrdersServerBeforeAgents: the orchestrator provisions the server
// FIRST (even when members arrive scrambled), awaits it to ready, THEN the agents,
// and records the durable set status ready. Reuses the single-guest engine per member.
func TestSubmitSetOrdersServerBeforeAgents(t *testing.T) {
	created := make(chan proxmox.UPID, 8)
	mock := &proxmoxtest.MockClient{
		OnCreateVM: func(_ context.Context, _ string, params map[string]any) (proxmox.UPID, error) {
			u := proxmox.UPID(fmt.Sprintf("UPID:pve01:1:1:1:qmcreate:%v:u@pam:", params["vmid"]))
			created <- u
			return u, nil
		},
		OnGuestAction: func(_ context.Context, ref proxmox.GuestRef, _ string) (proxmox.UPID, error) {
			u := proxmox.UPID(fmt.Sprintf("UPID:pve01:2:2:2:qmstart:%d:u@pam:", ref.VMID))
			created <- u
			return u, nil
		},
		OnAgentInterfaces: func(context.Context, proxmox.GuestRef) ([]types.GuestNIC, error) {
			return []types.GuestNIC{{Name: "eth0", IPv4: []string{"10.0.0.9/24"}}}, nil
		},
	}
	e, reg := testEngine(mock)
	writer := &orderedWriter{}
	e.Snippets = writer
	e.ConfigurePoll = 5 * time.Millisecond
	e.ConfigureTimeout = 3 * time.Second
	e.ProbeTimeout = 20 * time.Millisecond
	e.Probe = func(string, int, time.Duration) error { return nil }

	var fmu sync.Mutex
	finalized := map[string]bool{}
	e.Finalize = func(_ context.Context, id, _ string) error {
		fmu.Lock()
		finalized[id] = true
		fmu.Unlock()
		return nil
	}
	e.Release = func(context.Context, string) error { return nil }

	// Scrambled order (agent, server, agent) proves the orchestrator sorts server first.
	members := []SetMember{
		{Role: "agent", Req: setMemberReq(202, "c1-agent-1", &types.IPConfig{Mode: "dhcp"}), SnippetContent: "#cloud-config\n# agent 202\n", SnippetFilename: "proxcloud-202-k3s-cluster.yaml", ReadinessPort: 0, OwnershipID: "own-202"},
		{Role: "server", Req: setMemberReq(201, "c1-server", &types.IPConfig{Mode: "static", CIDR: "192.168.1.50/24", Gateway: "192.168.1.1"}), SnippetContent: "#cloud-config\n# server 201\n", SnippetFilename: "proxcloud-201-k3s-cluster.yaml", ReadinessPort: 6443, OwnershipID: "own-201"},
		{Role: "agent", Req: setMemberReq(203, "c1-agent-2", &types.IPConfig{Mode: "dhcp"}), SnippetContent: "#cloud-config\n# agent 203\n", SnippetFilename: "proxcloud-203-k3s-cluster.yaml", ReadinessPort: 0, OwnershipID: "own-203"},
	}

	statusCh := make(chan string, 2)
	hooks := SetHooks{UpdateStatus: func(_ context.Context, _, status string) error { statusCh <- status; return nil }}

	done := make(chan struct{})
	go func() {
		for {
			select {
			case u := <-created:
				completeTracked(reg, u, true)
			case <-done:
				return
			}
		}
	}()
	defer close(done)

	e.SubmitSet("set-1", members, SetContext{ServiceID: "k3s-cluster", ProjectID: "proj-1"}, hooks)

	select {
	case s := <-statusCh:
		if s != "ready" {
			t.Fatalf("set status = %q, want ready", s)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("set never reached a terminal status")
	}

	order := writer.order()
	if len(order) != 3 {
		t.Fatalf("snippet writes = %v, want 3", order)
	}
	if order[0] != "proxcloud-201-k3s-cluster.yaml" {
		t.Fatalf("server snippet must be written FIRST; got order %v", order)
	}
	for _, id := range []string{"own-201", "own-202", "own-203"} {
		if !finalized[id] {
			t.Errorf("member %s ownership was not finalized (should be, it succeeded)", id)
		}
	}
}

// TestSubmitSetServerFailureSkipsAgents: a control-plane create failure marks the
// set failed and SKIPS the agents (nothing to join), releasing their still-pending
// reservations so no quota leaks. The successful-member preservation is the sibling
// test below; here there are none.
func TestSubmitSetServerFailureSkipsAgents(t *testing.T) {
	var mu sync.Mutex
	createVMCount := 0
	mock := &proxmoxtest.MockClient{
		OnCreateVM: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			mu.Lock()
			createVMCount++
			mu.Unlock()
			return "", errors.New("no space left on the storage")
		},
	}
	e, _ := testEngine(mock)
	e.Snippets = &orderedWriter{}

	var rmu sync.Mutex
	released := map[string]bool{}
	e.Release = func(_ context.Context, id string) error { rmu.Lock(); released[id] = true; rmu.Unlock(); return nil }
	e.Finalize = func(context.Context, string, string) error { return nil }

	members := []SetMember{
		{Role: "server", Req: setMemberReq(201, "c1-server", &types.IPConfig{Mode: "static", CIDR: "192.168.1.50/24"}), SnippetContent: "#cloud-config\n", SnippetFilename: "proxcloud-201-k3s-cluster.yaml", ReadinessPort: 6443, OwnershipID: "own-201"},
		{Role: "agent", Req: setMemberReq(202, "c1-agent-1", &types.IPConfig{Mode: "dhcp"}), SnippetContent: "#cloud-config\n", SnippetFilename: "proxcloud-202-k3s-cluster.yaml", OwnershipID: "own-202"},
		{Role: "agent", Req: setMemberReq(203, "c1-agent-2", &types.IPConfig{Mode: "dhcp"}), SnippetContent: "#cloud-config\n", SnippetFilename: "proxcloud-203-k3s-cluster.yaml", OwnershipID: "own-203"},
	}
	statusCh := make(chan string, 2)
	hooks := SetHooks{UpdateStatus: func(_ context.Context, _, status string) error { statusCh <- status; return nil }}

	e.SubmitSet("set-1", members, SetContext{ServiceID: "k3s-cluster"}, hooks)

	select {
	case s := <-statusCh:
		if s != "failed" {
			t.Fatalf("set status = %q, want failed", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("set never reached a terminal status")
	}

	mu.Lock()
	gotCreates := createVMCount
	mu.Unlock()
	if gotCreates != 1 {
		t.Fatalf("CreateVM called %d times, want 1 (agents must be skipped once the control plane fails)", gotCreates)
	}
	rmu.Lock()
	defer rmu.Unlock()
	for _, id := range []string{"own-201", "own-202", "own-203"} {
		if !released[id] {
			t.Errorf("reservation %s was not released (server release + skipped-agent releases must all fire)", id)
		}
	}
}

// TestSubmitSetMemberFailurePreservesSuccessful: the server + first agent come up,
// the second agent's create fails. Because the control plane is UP, the set is
// marked DEGRADED (a live-but-partial cluster) — not failed (which is reserved for
// a control-plane failure). The successful members are FINALIZED (preserved — never
// auto-destroyed, ADR-0029), and only the failed member's reservation is released.
func TestSubmitSetMemberFailurePreservesSuccessful(t *testing.T) {
	created := make(chan proxmox.UPID, 8)
	mock := &proxmoxtest.MockClient{
		OnCreateVM: func(_ context.Context, _ string, params map[string]any) (proxmox.UPID, error) {
			if fmt.Sprintf("%v", params["vmid"]) == "203" {
				return "", errors.New("no space left on the storage")
			}
			u := proxmox.UPID(fmt.Sprintf("UPID:pve01:1:1:1:qmcreate:%v:u@pam:", params["vmid"]))
			created <- u
			return u, nil
		},
		OnGuestAction: func(_ context.Context, ref proxmox.GuestRef, _ string) (proxmox.UPID, error) {
			u := proxmox.UPID(fmt.Sprintf("UPID:pve01:2:2:2:qmstart:%d:u@pam:", ref.VMID))
			created <- u
			return u, nil
		},
		OnAgentInterfaces: func(context.Context, proxmox.GuestRef) ([]types.GuestNIC, error) {
			return []types.GuestNIC{{Name: "eth0", IPv4: []string{"10.0.0.9/24"}}}, nil
		},
	}
	e, reg := testEngine(mock)
	e.Snippets = &orderedWriter{}
	e.ConfigurePoll = 5 * time.Millisecond
	e.ConfigureTimeout = 3 * time.Second
	e.ProbeTimeout = 20 * time.Millisecond
	e.Probe = func(string, int, time.Duration) error { return nil }

	var fmu sync.Mutex
	finalized := map[string]bool{}
	released := map[string]bool{}
	e.Finalize = func(_ context.Context, id, _ string) error {
		fmu.Lock()
		finalized[id] = true
		fmu.Unlock()
		return nil
	}
	e.Release = func(_ context.Context, id string) error { fmu.Lock(); released[id] = true; fmu.Unlock(); return nil }

	members := []SetMember{
		{Role: "server", Req: setMemberReq(201, "c1-server", &types.IPConfig{Mode: "static", CIDR: "192.168.1.50/24"}), SnippetContent: "#cloud-config\n", SnippetFilename: "proxcloud-201-k3s-cluster.yaml", ReadinessPort: 6443, OwnershipID: "own-201"},
		{Role: "agent", Req: setMemberReq(202, "c1-agent-1", &types.IPConfig{Mode: "dhcp"}), SnippetContent: "#cloud-config\n", SnippetFilename: "proxcloud-202-k3s-cluster.yaml", OwnershipID: "own-202"},
		{Role: "agent", Req: setMemberReq(203, "c1-agent-2", &types.IPConfig{Mode: "dhcp"}), SnippetContent: "#cloud-config\n", SnippetFilename: "proxcloud-203-k3s-cluster.yaml", OwnershipID: "own-203"},
	}
	statusCh := make(chan string, 2)
	hooks := SetHooks{UpdateStatus: func(_ context.Context, _, status string) error { statusCh <- status; return nil }}

	done := make(chan struct{})
	go func() {
		for {
			select {
			case u := <-created:
				completeTracked(reg, u, true)
			case <-done:
				return
			}
		}
	}()
	defer close(done)

	e.SubmitSet("set-1", members, SetContext{ServiceID: "k3s-cluster"}, hooks)

	select {
	case s := <-statusCh:
		if s != "degraded" {
			t.Fatalf("set status = %q, want degraded (server up, one agent failed)", s)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("set never reached a terminal status")
	}

	fmu.Lock()
	defer fmu.Unlock()
	if !finalized["own-201"] || !finalized["own-202"] {
		t.Errorf("successful members must be preserved (finalized): finalized=%v", finalized)
	}
	if finalized["own-203"] {
		t.Error("the failed member must NOT be finalized")
	}
	if !released["own-203"] {
		t.Errorf("the failed member's reservation must be released: released=%v", released)
	}
	if released["own-201"] || released["own-202"] {
		t.Error("successful members must NOT be released (no auto-destroy, ADR-0029)")
	}
}

// TestSubmitSetPanicRecovered: a panic in a member step (here, releasing a rejected
// member's reservation) is RECOVERED — the backend does not crash — and the set is
// marked failed (honest terminal state). Guards the longer multi-Proxmox set path,
// mirroring scheduler.runHandler's handler recover (commit 2565212).
func TestSubmitSetPanicRecovered(t *testing.T) {
	e, _ := testEngine(&proxmoxtest.MockClient{})
	e.Snippets = &orderedWriter{}

	// Releasing a member's reservation panics — a latent member-step bug that must not
	// take the whole process down.
	e.Release = func(context.Context, string) error { panic("boom releasing member") }
	e.Finalize = func(context.Context, string, string) error { return nil }

	// A server member with Cores=0 is rejected by Submit's Validate, so runSet reaches
	// releaseMember synchronously on its own (panicking) goroutine stack.
	bad := setMemberReq(201, "c1-server", &types.IPConfig{Mode: "static", CIDR: "192.168.1.50/24"})
	bad.Cores = 0
	members := []SetMember{
		{Role: "server", Req: bad, SnippetContent: "#cloud-config\n", SnippetFilename: "proxcloud-201-k3s-cluster.yaml", ReadinessPort: 6443, OwnershipID: "own-201"},
	}

	statusCh := make(chan string, 2)
	hooks := SetHooks{UpdateStatus: func(_ context.Context, _, status string) error { statusCh <- status; return nil }}

	e.SubmitSet("set-1", members, SetContext{ServiceID: "k3s-cluster"}, hooks)

	select {
	case s := <-statusCh:
		if s != "failed" {
			t.Fatalf("set status = %q, want failed after a recovered panic", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("panic was not recovered: the set never reached a terminal status")
	}
}
