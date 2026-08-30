package deploy

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

// fakeSnippetWriter records snippet writes/removes for the catalog engine tests.
type fakeSnippetWriter struct {
	mu       sync.Mutex
	writeErr error
	written  map[string]string
	removed  []string
}

func (f *fakeSnippetWriter) WriteSnippet(_ context.Context, name, content string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeErr != nil {
		return f.writeErr
	}
	if f.written == nil {
		f.written = map[string]string{}
	}
	f.written[name] = content
	return nil
}

func (f *fakeSnippetWriter) RemoveSnippet(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	return nil
}

func (f *fakeSnippetWriter) wroteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.written)
}

func (f *fakeSnippetWriter) removedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.removed)
}

// catalogReq is a qemu create carrying a catalog snippet reference, started so
// the configuring step can reach it.
func catalogReq() *types.CreateGuestRequest {
	req := vmReq()
	req.StartAfterCreate = true
	req.Catalog = &types.CatalogProvision{
		ServiceID:      "postgresql",
		SnippetRef:     "local:snippets/proxcloud-106-postgresql.yaml",
		Ports:          []int{5432},
		CredentialHint: "user postgres — password shown once at creation",
	}
	return req
}

func stepByKey(d *types.Deployment, key string) (types.DeploymentStep, bool) {
	for _, s := range d.Steps {
		if s.Key == key {
			return s, true
		}
	}
	return types.DeploymentStep{}, false
}

func testEngine(mock *proxmoxtest.MockClient) (*Engine, *tasks.Registry) {
	reg := tasks.NewRegistry()
	return NewEngine(mock, reg, events.NewBroker(), slog.New(slog.NewTextHandler(io.Discard, nil))), reg
}

// waitTracked blocks until upid is a running (tracked) task, so a subsequent
// reg.Complete is never dropped by a Complete-before-Track race. Complete only
// records tracked tasks (production's watcher only ever completes tasks it
// already found in the running set); a test that completes before the engine's
// Track would silently lose the outcome and hang. Poll like the handlers test.
func waitTracked(t *testing.T, reg *tasks.Registry, upid proxmox.UPID) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, u := range reg.Running() {
			if u == upid {
				return
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("task %s was never tracked", upid)
}

// awaitStatus polls Get until the deployment leaves "running" or times out.
func awaitStatus(t *testing.T, e *Engine, id string) *types.Deployment {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d, ok := e.Get(id); ok && d.Status != "running" {
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	d, _ := e.Get(id)
	t.Fatalf("deployment never finished: %+v", d)
	return nil
}

func TestEngineCreateAndStartSucceed(t *testing.T) {
	created := make(chan proxmox.UPID, 2)
	mock := &proxmoxtest.MockClient{
		OnCreateLXC: func(_ context.Context, node string, params map[string]any) (proxmox.UPID, error) {
			if node != "pve01" || params["hostname"] != "cache-01" {
				t.Errorf("create args: node=%q params=%v", node, params)
			}
			u := proxmox.UPID("UPID:pve01:1:1:1:vzcreate:200:u@pam:")
			created <- u
			return u, nil
		},
		OnGuestAction: func(_ context.Context, ref proxmox.GuestRef, action string) (proxmox.UPID, error) {
			if action != "start" || ref.VMID != 200 {
				t.Errorf("start args: %v %s", ref, action)
			}
			u := proxmox.UPID("UPID:pve01:2:2:2:vzstart:200:u@pam:")
			created <- u
			return u, nil
		},
	}
	e, reg := testEngine(mock)

	req := lxcReq()
	req.StartAfterCreate = true
	dep, err := e.Submit(req, CreateContext{})
	if err != nil {
		t.Fatal(err)
	}
	if len(dep.Steps) != 2 || dep.Status != "running" {
		t.Fatalf("initial deployment = %+v", dep)
	}

	// Simulate the watcher (the single PVE poller) completing each task. Wait
	// until the engine has tracked the task before completing it — Complete on an
	// untracked UPID is dropped, which would hang the engine (esp. under -race).
	for i := 0; i < 2; i++ {
		select {
		case u := <-created:
			waitTracked(t, reg, u)
			reg.Complete(u, true, "OK")
		case <-time.After(3 * time.Second):
			t.Fatalf("task %d never submitted", i)
		}
	}

	final := awaitStatus(t, e, dep.ID)
	if final.Status != "succeeded" {
		t.Fatalf("final = %+v", final)
	}
	for _, s := range final.Steps {
		if s.Status != "succeeded" || s.UPID == "" || s.StartedAt == nil || s.EndedAt == nil {
			t.Errorf("step %s = %+v", s.Key, s)
		}
	}
}

func TestEngineCreateTaskFails(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnCreateVM: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			return "UPID:pve01:3:3:3:qmcreate:106:u@pam:", nil
		},
	}
	e, reg := testEngine(mock)

	req := vmReq()
	req.StartAfterCreate = false
	dep, err := e.Submit(req, CreateContext{})
	if err != nil {
		t.Fatal(err)
	}
	// Watcher reports the real PVE failure once the task is tracked (no timing
	// race: wait for Track, then Complete).
	upid := proxmox.UPID("UPID:pve01:3:3:3:qmcreate:106:u@pam:")
	waitTracked(t, reg, upid)
	reg.Complete(upid, false, "unable to create image: no space left")

	final := awaitStatus(t, e, dep.ID)
	if final.Status != "failed" {
		t.Fatalf("final = %+v", final)
	}
	if final.Steps[0].Status != "failed" || final.Steps[0].Message != "unable to create image: no space left" {
		t.Errorf("failed step must carry the verbatim PVE error: %+v", final.Steps[0])
	}
}

func TestEngineSubmitErrorSurfacesPVEMessage(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnCreateLXC: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			return "", &types.APIError{Code: "proxmox_error", Message: "Proxmox rejected the request.", PVEMessage: "CT 200 already exists", Status: 502}
		},
	}
	e, _ := testEngine(mock)

	dep, err := e.Submit(lxcReq(), CreateContext{})
	if err != nil {
		t.Fatal(err)
	}
	final := awaitStatus(t, e, dep.ID)
	if final.Status != "failed" || final.Steps[0].Message != "CT 200 already exists" {
		t.Fatalf("final = %+v", final)
	}
}

func TestEngineSubmitRejectsInvalid(t *testing.T) {
	e, _ := testEngine(&proxmoxtest.MockClient{})
	req := vmReq()
	req.Name = "Bad Name"
	if _, err := e.Submit(req, CreateContext{}); err == nil {
		t.Fatal("invalid request accepted")
	}
}

// TestEngineCatalogSnippetWriteFails: a snippet write failure fails the
// deployment BEFORE CreateVM and releases the ownership reservation (ADR-0025).
func TestEngineCatalogSnippetWriteFails(t *testing.T) {
	var createCalled bool
	mock := &proxmoxtest.MockClient{
		OnCreateVM: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			createCalled = true
			return "UPID:pve01:1:1:1:qmcreate:106:u@pam:", nil
		},
	}
	e, _ := testEngine(mock)
	writer := &fakeSnippetWriter{writeErr: errors.New("sftp: connection refused")}
	e.Snippets = writer

	var released string
	e.Release = func(_ context.Context, id string) error { released = id; return nil }

	dep, err := e.Submit(catalogReq(), CreateContext{OwnershipID: "own-1"})
	if err != nil {
		t.Fatal(err)
	}
	final := awaitStatus(t, e, dep.ID)
	if final.Status != "failed" {
		t.Fatalf("status = %q, want failed", final.Status)
	}
	prep, ok := stepByKey(final, "prepare")
	if !ok || prep.Status != "failed" || prep.Message == "" {
		t.Fatalf("prepare step = %+v, want failed with the writer error", prep)
	}
	if createCalled {
		t.Error("CreateVM was called despite the snippet write failing (must fail before any Proxmox call)")
	}
	if released != "own-1" {
		t.Errorf("ReleaseOwnership got %q, want own-1 (reservation must be freed)", released)
	}
	if writer.removedCount() != 0 {
		t.Errorf("nothing was written, so nothing should be removed (got %d)", writer.removedCount())
	}
}

// TestEngineCatalogReachesReady: the happy path writes the snippet, creates and
// starts the VM, then the configuring step waits for an agent IP and surfaces the
// connection details before declaring ready. The snippet is NOT removed on
// success.
func TestEngineCatalogReachesReady(t *testing.T) {
	created := make(chan proxmox.UPID, 2)
	mock := &proxmoxtest.MockClient{
		OnCreateVM: func(_ context.Context, node string, params map[string]any) (proxmox.UPID, error) {
			if params["cicustom"] != "user=local:snippets/proxcloud-106-postgresql.yaml" {
				t.Errorf("cicustom = %v", params["cicustom"])
			}
			u := proxmox.UPID("UPID:pve01:1:1:1:qmcreate:106:u@pam:")
			created <- u
			return u, nil
		},
		OnGuestAction: func(_ context.Context, ref proxmox.GuestRef, action string) (proxmox.UPID, error) {
			u := proxmox.UPID("UPID:pve01:2:2:2:qmstart:106:u@pam:")
			created <- u
			return u, nil
		},
		OnAgentInterfaces: func(context.Context, proxmox.GuestRef) ([]types.GuestNIC, error) {
			return []types.GuestNIC{{Name: "eth0", IPv4: []string{"10.0.0.5/24"}}}, nil
		},
	}
	e, reg := testEngine(mock)
	writer := &fakeSnippetWriter{}
	e.Snippets = writer
	// Keep the configuring poll + probe fast for the test.
	e.ConfigurePoll = 5 * time.Millisecond
	e.ConfigureTimeout = 3 * time.Second
	e.ProbeTimeout = 20 * time.Millisecond
	// The readiness port answers immediately (no live listener in the test).
	e.Probe = func(string, int, time.Duration) error { return nil }

	var finalized string
	e.Finalize = func(_ context.Context, id, _ string) error { finalized = id; return nil }

	dep, err := e.Submit(catalogReq(), CreateContext{OwnershipID: "own-1", SnippetFilename: "proxcloud-106-postgresql.yaml", SnippetContent: "#cloud-config\n", ReadinessPort: 5432})
	if err != nil {
		t.Fatal(err)
	}
	// The initial steps: prepare, create, start, configuring.
	if len(dep.Steps) != 4 {
		t.Fatalf("steps = %d, want 4 (prepare/create/start/configuring): %+v", len(dep.Steps), dep.Steps)
	}

	// Complete the create then start tasks (as the watcher would).
	for i := 0; i < 2; i++ {
		select {
		case u := <-created:
			waitTracked(t, reg, u)
			reg.Complete(u, true, "OK")
		case <-time.After(3 * time.Second):
			t.Fatalf("task %d never submitted", i)
		}
	}

	final := awaitStatus(t, e, dep.ID)
	if final.Status != "succeeded" {
		t.Fatalf("status = %q, want succeeded: %+v", final.Status, final)
	}
	conf, ok := stepByKey(final, "configuring")
	if !ok || conf.Status != "succeeded" {
		t.Fatalf("configuring step = %+v, want succeeded", conf)
	}
	if final.Connection != "10.0.0.5:5432" {
		t.Errorf("connection = %q, want 10.0.0.5:5432", final.Connection)
	}
	if len(final.Ports) != 1 || final.Ports[0] != 5432 {
		t.Errorf("ports = %v, want [5432]", final.Ports)
	}
	if final.CredentialHint == "" {
		t.Error("credential hint should be set")
	}
	if finalized != "own-1" {
		t.Errorf("FinalizeOwnership got %q, want own-1", finalized)
	}
	if writer.wroteCount() != 1 {
		t.Errorf("snippet write count = %d, want 1", writer.wroteCount())
	}
	if writer.removedCount() != 0 {
		t.Errorf("snippet must NOT be removed on success (got %d removes)", writer.removedCount())
	}
}

// TestEngineCatalogReadinessNeverPasses: the guest boots (agent reports an IP) but
// its service port never becomes reachable. The configuring step must FAIL with an
// honest message and fail the deployment — never a silent ready (ADR-0028,
// CLAUDE.md rule 5). Ownership was finalized at create (the guest exists) and the
// snippet is cleaned up on the failure path.
func TestEngineCatalogReadinessNeverPasses(t *testing.T) {
	created := make(chan proxmox.UPID, 2)
	mock := &proxmoxtest.MockClient{
		OnCreateVM: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			u := proxmox.UPID("UPID:pve01:1:1:1:qmcreate:106:u@pam:")
			created <- u
			return u, nil
		},
		OnGuestAction: func(context.Context, proxmox.GuestRef, string) (proxmox.UPID, error) {
			u := proxmox.UPID("UPID:pve01:2:2:2:qmstart:106:u@pam:")
			created <- u
			return u, nil
		},
		// The agent reports an IP (the OS booted) ...
		OnAgentInterfaces: func(context.Context, proxmox.GuestRef) ([]types.GuestNIC, error) {
			return []types.GuestNIC{{Name: "eth0", IPv4: []string{"10.0.0.5/24"}}}, nil
		},
	}
	e, reg := testEngine(mock)
	writer := &fakeSnippetWriter{}
	e.Snippets = writer
	e.ConfigurePoll = 5 * time.Millisecond
	e.ConfigureTimeout = 80 * time.Millisecond
	e.ProbeTimeout = 5 * time.Millisecond
	// ... but the service port never answers.
	e.Probe = func(string, int, time.Duration) error { return errors.New("connection refused") }

	var finalized string
	e.Finalize = func(_ context.Context, id, _ string) error { finalized = id; return nil }

	dep, err := e.Submit(catalogReq(), CreateContext{OwnershipID: "own-1", SnippetFilename: "proxcloud-106-postgresql.yaml", SnippetContent: "#cloud-config\n", ReadinessPort: 5432})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		select {
		case u := <-created:
			waitTracked(t, reg, u)
			reg.Complete(u, true, "OK")
		case <-time.After(3 * time.Second):
			t.Fatalf("task %d never submitted", i)
		}
	}

	final := awaitStatus(t, e, dep.ID)
	if final.Status != "failed" {
		t.Fatalf("status = %q, want failed (readiness never passed): %+v", final.Status, final)
	}
	conf, ok := stepByKey(final, "configuring")
	if !ok || conf.Status != "failed" {
		t.Fatalf("configuring step = %+v, want failed", conf)
	}
	// The message must be honest: it names the port and the wait budget.
	if !strings.Contains(conf.Message, "5432") || !strings.Contains(conf.Message, "never became reachable") {
		t.Errorf("configuring message must honestly name the unreachable port: %q", conf.Message)
	}
	// A failed-configuring guest still exists on Proxmox, so its ownership was
	// finalized at create (never silently released).
	if finalized != "own-1" {
		t.Errorf("ownership must be finalized at create even if configuring fails, got %q", finalized)
	}
	// The snippet is cleaned up on the failure path.
	if writer.removedCount() != 1 {
		t.Errorf("snippet must be removed on the configuring-failure path (got %d removes)", writer.removedCount())
	}
	// No connection is surfaced for a guest that never became reachable.
	if final.Connection != "" {
		t.Errorf("connection must be empty on readiness failure, got %q", final.Connection)
	}
}
