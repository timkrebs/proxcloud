package deploy

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

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
