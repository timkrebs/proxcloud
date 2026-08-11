package deploy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

// stepTimeout bounds one deployment step (large full clones can be slow).
const stepTimeout = 30 * time.Minute

// Engine executes deployments: create/clone (+ optional start) with live
// per-step progress. Deployments are kept in memory — the guest itself and
// the Proxmox task log remain the durable truth.
type Engine struct {
	PVE      proxmox.Client
	Registry *tasks.Registry
	Broker   *events.Broker
	Log      *slog.Logger

	mu   sync.RWMutex
	runs map[string]*types.Deployment
}

// NewEngine returns an empty engine.
func NewEngine(pve proxmox.Client, reg *tasks.Registry, broker *events.Broker, log *slog.Logger) *Engine {
	return &Engine{PVE: pve, Registry: reg, Broker: broker, Log: log, runs: map[string]*types.Deployment{}}
}

// Get returns a deployment snapshot by id.
func (e *Engine) Get(id string) (*types.Deployment, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	d, ok := e.runs[id]
	if !ok {
		return nil, false
	}
	cp := *d
	cp.Steps = append([]types.DeploymentStep(nil), d.Steps...)
	return &cp, true
}

// Submit validates the request and starts the deployment goroutine.
func (e *Engine) Submit(req *types.CreateGuestRequest) (*types.Deployment, error) {
	if err := Validate(req); err != nil {
		return nil, &types.APIError{Code: "invalid_request", Message: err.Error(), Status: 400}
	}

	kind := "virtual machine"
	if req.Type == "lxc" {
		kind = "container"
	}
	createLabel := fmt.Sprintf("Create %s %s", kind, req.Name)
	if req.Source.Mode == "clone" {
		createLabel = fmt.Sprintf("Clone VMID %d into %s", req.Source.CloneVMID, req.Name)
	}

	buf := make([]byte, 6)
	_, _ = rand.Read(buf)
	dep := &types.Deployment{
		ID:        "dep_" + hex.EncodeToString(buf),
		Name:      req.Name,
		Type:      req.Type,
		Node:      req.Node,
		VMID:      req.VMID,
		Status:    "running",
		CreatedAt: time.Now().UTC(),
		Steps:     []types.DeploymentStep{{Key: "create", Label: createLabel, Status: "pending"}},
	}
	if req.StartAfterCreate {
		dep.Steps = append(dep.Steps, types.DeploymentStep{
			Key: "start", Label: fmt.Sprintf("Start %s %s", kind, req.Name), Status: "pending",
		})
	}

	e.mu.Lock()
	e.runs[dep.ID] = dep
	e.mu.Unlock()

	go e.run(dep.ID, req)
	snapshot, _ := e.Get(dep.ID)
	return snapshot, nil
}

// run executes the deployment steps sequentially.
func (e *Engine) run(id string, req *types.CreateGuestRequest) {
	ctx := context.Background()
	res := types.TaskResource{Type: req.Type, VMID: req.VMID, Node: req.Node, Name: req.Name}

	submitCreate := func() (proxmox.UPID, error) {
		if req.Source.Mode == "clone" {
			cp, err := BuildCloneParams(req)
			if err != nil {
				return "", &types.APIError{Code: "invalid_request", Message: err.Error(), Status: 400}
			}
			src := proxmox.GuestRef{Node: req.Source.CloneNode, Type: "qemu", VMID: req.Source.CloneVMID}
			if src.Node == "" {
				src.Node = req.Node
			}
			return e.PVE.CloneGuest(ctx, src, cp.NewVMID, cp.Name, cp.Pool, cp.Full, cp.Storage)
		}
		params, err := BuildCreateParams(req)
		if err != nil {
			return "", &types.APIError{Code: "invalid_request", Message: err.Error(), Status: 400}
		}
		if req.Type == "lxc" {
			return e.PVE.CreateLXC(ctx, req.Node, params)
		}
		return e.PVE.CreateVM(ctx, req.Node, params)
	}

	label := e.stepLabel(id, "create")
	upid, err := submitCreate()
	if err != nil {
		e.failStep(id, "create", err)
		return
	}
	e.Registry.Track(upid, label, "provisioning", res)
	e.updateStep(id, "create", "running", string(upid), "")
	if !e.awaitTask(id, "create", upid) {
		return
	}

	if req.StartAfterCreate {
		ref := proxmox.GuestRef{Node: req.Node, Type: req.Type, VMID: req.VMID}
		startUPID, err := e.PVE.GuestAction(ctx, ref, "start")
		if err != nil {
			e.failStep(id, "start", err)
			return
		}
		e.Registry.Track(startUPID, e.stepLabel(id, "start"), "starting", res)
		e.updateStep(id, "start", "running", string(startUPID), "")
		if !e.awaitTask(id, "start", startUPID) {
			return
		}
	}

	e.finish(id, "succeeded")
}

// awaitTask waits for the tracked task to finish. The tasks.Watcher is
// the single PVE poller — its Complete() call delivers the outcome here,
// so the deployment page, the notification bell, and this engine all see
// the same result without double-polling Proxmox.
func (e *Engine) awaitTask(id, step string, upid proxmox.UPID) bool {
	ctx, cancel := context.WithTimeout(context.Background(), stepTimeout)
	defer cancel()
	outcome, err := e.Registry.AwaitCompletion(ctx, upid)
	if err != nil {
		e.updateStep(id, step, "failed", string(upid), "timed out waiting for the Proxmox task")
		e.finish(id, "failed")
		return false
	}
	if outcome.Succeeded {
		e.updateStep(id, step, "succeeded", string(upid), "")
		return true
	}
	e.updateStep(id, step, "failed", string(upid), outcome.ExitStatus)
	e.finish(id, "failed")
	return false
}

func (e *Engine) stepLabel(id, key string) string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	for _, s := range e.runs[id].Steps {
		if s.Key == key {
			return s.Label
		}
	}
	return key
}

func (e *Engine) failStep(id, key string, err error) {
	msg := err.Error()
	var apiErr *types.APIError
	if errors.As(err, &apiErr) {
		msg = apiErr.Message
		if apiErr.PVEMessage != "" {
			msg = apiErr.PVEMessage
		}
	}
	e.updateStep(id, key, "failed", "", msg)
	e.finish(id, "failed")
}

func (e *Engine) updateStep(id, key, status, upid, msg string) {
	now := time.Now().UTC()
	e.mu.Lock()
	if d, ok := e.runs[id]; ok {
		for i := range d.Steps {
			if d.Steps[i].Key != key {
				continue
			}
			d.Steps[i].Status = status
			if upid != "" {
				d.Steps[i].UPID = upid
			}
			if msg != "" {
				d.Steps[i].Message = msg
			}
			if status == "running" && d.Steps[i].StartedAt == nil {
				d.Steps[i].StartedAt = &now
			}
			if status == "succeeded" || status == "failed" {
				d.Steps[i].EndedAt = &now
			}
		}
	}
	e.mu.Unlock()
	e.publish(id)
}

func (e *Engine) finish(id, status string) {
	e.mu.Lock()
	if d, ok := e.runs[id]; ok {
		d.Status = status
	}
	e.mu.Unlock()
	e.publish(id)
}

func (e *Engine) publish(id string) {
	if e.Broker == nil {
		return
	}
	if d, ok := e.Get(id); ok {
		e.Broker.Publish(events.Event{Name: "deployment", Data: d})
	}
}
