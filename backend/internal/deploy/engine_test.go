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
	dep, err := e.Submit(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(dep.Steps) != 2 || dep.Status != "running" {
		t.Fatalf("initial deployment = %+v", dep)
	}

	// Simulate the watcher (the single PVE poller) completing each task.
	for i := 0; i < 2; i++ {
		select {
		case u := <-created:
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
	dep, err := e.Submit(req)
	if err != nil {
		t.Fatal(err)
	}
	// Watcher reports the real PVE failure.
	go func() {
		time.Sleep(20 * time.Millisecond)
		reg.Complete("UPID:pve01:3:3:3:qmcreate:106:u@pam:", false, "unable to create image: no space left")
	}()

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

	dep, err := e.Submit(lxcReq())
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
	if _, err := e.Submit(req); err == nil {
		t.Fatal("invalid request accepted")
	}
}
