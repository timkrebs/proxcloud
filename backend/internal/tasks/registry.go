// Package tasks tracks the Proxmox tasks Proxcloud itself initiated: their
// transitional guest status (starting/stopping/...), and the notification
// ring behind the bell pane. The global activity log does NOT come from
// here — it proxies /cluster/tasks so a backend restart never loses truth.
package tasks

import (
	"context"
	"fmt"
	"sync"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
)

const (
	notificationCap = 200
	completionCap   = 500
)

// Outcome is a finished task's result, delivered to AwaitCompletion.
type Outcome struct {
	Succeeded  bool
	ExitStatus string
}

// Tracked is one Proxcloud-initiated task in flight.
type Tracked struct {
	UPID         proxmox.UPID
	Action       string // friendly label, e.g. "Start virtual machine"
	Transitional string // guest status override while running
	Resource     types.TaskResource
	notifID      string
}

// Registry is safe for concurrent use.
type Registry struct {
	mu        sync.RWMutex
	running   map[proxmox.UPID]*Tracked
	completed map[proxmox.UPID]Outcome
	waiters   map[proxmox.UPID][]chan Outcome
	notifs    []types.Notification // newest first
	nextID    int
	now       func() time.Time
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		running:   make(map[proxmox.UPID]*Tracked),
		completed: make(map[proxmox.UPID]Outcome),
		waiters:   make(map[proxmox.UPID][]chan Outcome),
		now:       time.Now,
	}
}

// Track registers a just-submitted task and creates its running
// notification. Detail should name the resource, e.g. "web-01 (VMID 101)".
func (r *Registry) Track(upid proxmox.UPID, action, transitional string, res types.TaskResource) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	id := fmt.Sprintf("n%d", r.nextID)
	name := res.Name
	if name == "" {
		name = fmt.Sprintf("%s/%d", res.Type, res.VMID)
	}
	r.running[upid] = &Tracked{
		UPID: upid, Action: action, Transitional: transitional, Resource: res, notifID: id,
	}
	r.prependNotif(types.Notification{
		ID:        id,
		Kind:      "prog",
		Title:     action,
		Detail:    fmt.Sprintf("%s (VMID %d) · in progress", name, res.VMID),
		UPID:      string(upid),
		Status:    "running",
		CreatedAt: r.now().UTC(),
	})
}

// Complete finalizes a tracked task: flips its notification to ok/err (err
// carries PVE's verbatim exit status) and removes the transitional overlay.
// It returns the tracked entry, or nil if the UPID was not tracked.
func (r *Registry) Complete(upid proxmox.UPID, succeeded bool, exitStatus string) *Tracked {
	r.mu.Lock()
	defer r.mu.Unlock()

	tr, ok := r.running[upid]
	if !ok {
		return nil
	}
	delete(r.running, upid)

	// Record the outcome for AwaitCompletion (the deploy engine waits here
	// instead of running its own PVE poll — the watcher is the single
	// poller) and wake any waiters.
	outcome := Outcome{Succeeded: succeeded, ExitStatus: exitStatus}
	if len(r.completed) >= completionCap {
		r.completed = make(map[proxmox.UPID]Outcome) // bounded memory; waiters were already served
	}
	r.completed[upid] = outcome
	for _, ch := range r.waiters[upid] {
		ch <- outcome
	}
	delete(r.waiters, upid)

	for i := range r.notifs {
		if r.notifs[i].ID != tr.notifID {
			continue
		}
		name := tr.Resource.Name
		if name == "" {
			name = fmt.Sprintf("%s/%d", tr.Resource.Type, tr.Resource.VMID)
		}
		if succeeded {
			r.notifs[i].Kind = "ok"
			r.notifs[i].Status = "succeeded"
			r.notifs[i].Detail = fmt.Sprintf("%s (VMID %d) · completed successfully", name, tr.Resource.VMID)
		} else {
			r.notifs[i].Kind = "err"
			r.notifs[i].Status = "failed"
			r.notifs[i].Detail = fmt.Sprintf("%s (VMID %d) · %s", name, tr.Resource.VMID, exitStatus)
		}
		r.notifs[i].Read = false
		break
	}
	return tr
}

// Running returns the UPIDs of all in-flight tracked tasks.
func (r *Registry) Running() []proxmox.UPID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]proxmox.UPID, 0, len(r.running))
	for u := range r.running {
		out = append(out, u)
	}
	return out
}

// ActiveFor returns the transitional status + UPID of a running tracked
// task targeting vmid, if any.
func (r *Registry) ActiveFor(vmid int) (transitional string, upid string, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for u, tr := range r.running {
		if tr.Resource.VMID == vmid {
			return tr.Transitional, string(u), true
		}
	}
	return "", "", false
}

// Lookup returns the friendly action + resource for a tracked (running)
// UPID, for enriching the activity log.
func (r *Registry) Lookup(upid proxmox.UPID) (*Tracked, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tr, ok := r.running[upid]
	return tr, ok
}

// Notifications returns the ring, newest first.
func (r *Registry) Notifications() []types.Notification {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]types.Notification, len(r.notifs))
	copy(out, r.notifs)
	return out
}

// MarkRead flags the given notification ids as read.
func (r *Registry) MarkRead(ids []string) {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.notifs {
		if _, ok := set[r.notifs[i].ID]; ok {
			r.notifs[i].Read = true
		}
	}
}

// AwaitCompletion blocks until the tracked task finishes (delivered by
// Complete) or ctx is done. The completion record is consumed.
func (r *Registry) AwaitCompletion(ctx context.Context, upid proxmox.UPID) (Outcome, error) {
	r.mu.Lock()
	if o, ok := r.completed[upid]; ok {
		delete(r.completed, upid)
		r.mu.Unlock()
		return o, nil
	}
	ch := make(chan Outcome, 1)
	r.waiters[upid] = append(r.waiters[upid], ch)
	r.mu.Unlock()

	select {
	case <-ctx.Done():
		return Outcome{}, ctx.Err()
	case o := <-ch:
		return o, nil
	}
}

func (r *Registry) prependNotif(n types.Notification) {
	r.notifs = append([]types.Notification{n}, r.notifs...)
	if len(r.notifs) > notificationCap {
		r.notifs = r.notifs[:notificationCap]
	}
}
