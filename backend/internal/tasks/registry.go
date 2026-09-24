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

// Tracked is one Proxcloud-initiated task in flight. TenantID is the OWNING
// tenant of the target guest at Track time ("" = no tenant — such a task's
// notification is visible to platform admins only).
type Tracked struct {
	UPID         proxmox.UPID
	Action       string // friendly label, e.g. "Start virtual machine"
	Transitional string // guest status override while running
	Resource     types.TaskResource
	TenantID     string
	notifID      string
}

// notifEntry pairs a client notification with the VMID it concerns and the
// tenant that OWNED the guest when the entry was created. The ring is
// process-global (one Registry) and VMIDs are caller-chosen AND REUSED after a
// tombstone, so filtering by a live owned-VMID set would hand a freed VMID's
// history (guest names, verbatim PVE errors) to its next owner. The stored
// tenantID is the durable ownership fact the filter stands on (tenancy iron
// rule #1); an empty tenantID fails closed to admin-only visibility.
type notifEntry struct {
	n        types.Notification
	vmid     int
	tenantID string
}

// Registry is safe for concurrent use.
type Registry struct {
	mu        sync.RWMutex
	running   map[proxmox.UPID]*Tracked
	completed map[proxmox.UPID]Outcome
	waiters   map[proxmox.UPID][]chan Outcome
	notifs    []notifEntry // newest first
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

// Track registers a just-submitted task and creates its running notification.
// Detail should name the resource, e.g. "web-01 (VMID 101)". tenantID is the
// tenant that OWNS the target guest (deploy CreateContext, the handler's
// resolved scope, or the lifecycle ownership row); "" makes the notification
// platform-admin-only — the fail-closed default for untenanted work.
func (r *Registry) Track(upid proxmox.UPID, action, transitional string, res types.TaskResource, tenantID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	id := fmt.Sprintf("n%d", r.nextID)
	name := res.Name
	if name == "" {
		name = fmt.Sprintf("%s/%d", res.Type, res.VMID)
	}
	r.running[upid] = &Tracked{
		UPID: upid, Action: action, Transitional: transitional, Resource: res,
		TenantID: tenantID, notifID: id,
	}
	r.prependNotif(notifEntry{vmid: res.VMID, tenantID: tenantID, n: types.Notification{
		ID:        id,
		Kind:      "prog",
		Title:     action,
		Detail:    fmt.Sprintf("%s (VMID %d) · in progress", name, res.VMID),
		UPID:      string(upid),
		Status:    "running",
		CreatedAt: r.now().UTC(),
	}})
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
		if r.notifs[i].n.ID != tr.notifID {
			continue
		}
		name := tr.Resource.Name
		if name == "" {
			name = fmt.Sprintf("%s/%d", tr.Resource.Type, tr.Resource.VMID)
		}
		if succeeded {
			r.notifs[i].n.Kind = "ok"
			r.notifs[i].n.Status = "succeeded"
			r.notifs[i].n.Detail = fmt.Sprintf("%s (VMID %d) · completed successfully", name, tr.Resource.VMID)
		} else {
			r.notifs[i].n.Kind = "err"
			r.notifs[i].n.Status = "failed"
			r.notifs[i].n.Detail = fmt.Sprintf("%s (VMID %d) · %s", name, tr.Resource.VMID, exitStatus)
		}
		r.notifs[i].n.Read = false
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

// Notify prepends a standalone notification to the ring — one not bound to a PVE
// UPID. Missing ID/CreatedAt are filled so callers can pass a partial
// notification. It reuses the same bounded ring as tracked-task notifications.
//
// tenantID is the owning tenant, same contract as Track; an empty tenantID is
// visible ONLY to platform admins — the fail-closed default for notifications
// that concern no particular tenant. Currently unused; the scheduler warnings
// use the VMID-scoped SSE frames instead.
func (r *Registry) Notify(vmid int, tenantID string, n types.Notification) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if n.ID == "" {
		r.nextID++
		n.ID = fmt.Sprintf("n%d", r.nextID)
	}
	if n.CreatedAt.IsZero() {
		n.CreatedAt = r.now().UTC()
	}
	r.prependNotif(notifEntry{vmid: vmid, tenantID: tenantID, n: n})
}

// Notifications returns the ring newest-first, filtered to the caller's
// tenant. admin=true (platform admin) returns every entry. The filter compares
// the tenant STORED on each entry at Track time — not a live owned-VMID set —
// so a VMID freed by one tenant and reissued to another never carries the old
// tenant's history forward (iron rule #1). A caller with no active tenant sees
// nothing.
func (r *Registry) Notifications(tenantID string, admin bool) []types.Notification {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]types.Notification, 0, len(r.notifs))
	for _, e := range r.notifs {
		if admin || (tenantID != "" && e.tenantID == tenantID) {
			out = append(out, e.n)
		}
	}
	return out
}

// MarkRead flags the given notification ids as read, but only for entries
// belonging to the caller's tenant (admin=true for platform admin) — so one
// tenant cannot flip another tenant's notifications, across VMID reuse too.
func (r *Registry) MarkRead(ids []string, tenantID string, admin bool) {
	set := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.notifs {
		if _, ok := set[r.notifs[i].n.ID]; ok && (admin || (tenantID != "" && r.notifs[i].tenantID == tenantID)) {
			r.notifs[i].n.Read = true
		}
	}
}

// DropVMID removes every notification entry for vmid from the ring — the
// belt-and-suspenders cleanup on top of tenant filtering, called at the guest
// tombstone choke-points (user delete completion, TTL delete, deployment-set
// member destroy) so a freed VMID carries NOTHING forward to its next owner.
// The durable activity log (the /cluster/tasks proxy) is unaffected.
func (r *Registry) DropVMID(vmid int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.notifs[:0]
	for _, e := range r.notifs {
		if e.vmid != vmid {
			kept = append(kept, e)
		}
	}
	// Zero the tail so dropped entries don't linger in the backing array.
	for i := len(kept); i < len(r.notifs); i++ {
		r.notifs[i] = notifEntry{}
	}
	r.notifs = kept
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

func (r *Registry) prependNotif(e notifEntry) {
	r.notifs = append([]notifEntry{e}, r.notifs...)
	if len(r.notifs) > notificationCap {
		r.notifs = r.notifs[:notificationCap]
	}
}
