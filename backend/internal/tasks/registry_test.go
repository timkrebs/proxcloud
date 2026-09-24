package tasks

import (
	"fmt"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
)

func TestRegistryLifecycle(t *testing.T) {
	r := NewRegistry()
	res := types.TaskResource{Type: "qemu", VMID: 101, Node: "pve01", Name: "web-01"}
	r.Track("UPID:x:1:qmstart:", "Start virtual machine", "starting", res, "tenant-a")

	if st, upid, ok := r.ActiveFor(101); !ok || st != "starting" || upid != "UPID:x:1:qmstart:" {
		t.Fatalf("ActiveFor = %q %q %v", st, upid, ok)
	}
	if _, _, ok := r.ActiveFor(999); ok {
		t.Fatal("ActiveFor matched wrong vmid")
	}
	if len(r.Running()) != 1 {
		t.Fatalf("Running = %d, want 1", len(r.Running()))
	}

	n := r.Notifications("", true)
	if len(n) != 1 || n[0].Kind != "prog" || n[0].Status != "running" || n[0].Title != "Start virtual machine" {
		t.Fatalf("notification = %+v", n)
	}

	tr := r.Complete("UPID:x:1:qmstart:", true, "OK")
	if tr == nil || tr.Resource.Name != "web-01" || tr.TenantID != "tenant-a" {
		t.Fatalf("Complete returned %+v", tr)
	}
	if _, _, ok := r.ActiveFor(101); ok {
		t.Fatal("overlay survived completion")
	}
	n = r.Notifications("", true)
	if n[0].Kind != "ok" || n[0].Status != "succeeded" {
		t.Fatalf("completed notification = %+v", n[0])
	}
	if r.Complete("UPID:x:1:qmstart:", true, "OK") != nil {
		t.Fatal("double Complete returned a value")
	}
}

func TestRegistryFailureCarriesExitStatus(t *testing.T) {
	r := NewRegistry()
	r.Track("UPID:x:2:qmstop:", "Stop virtual machine", "stopping", types.TaskResource{Type: "qemu", VMID: 5, Name: "db"}, "tenant-a")
	r.Complete("UPID:x:2:qmstop:", false, "timeout waiting on systemd")

	n := r.Notifications("", true)[0]
	if n.Kind != "err" || n.Status != "failed" {
		t.Fatalf("notification = %+v", n)
	}
	if want := "db (VMID 5) · timeout waiting on systemd"; n.Detail != want {
		t.Errorf("detail = %q, want verbatim PVE error %q", n.Detail, want)
	}
}

func TestRegistryMarkReadAndCap(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < notificationCap+10; i++ {
		r.Track(proxmox.UPID(fmt.Sprintf("UPID:x:%d:vzstart:", i)), "Start container", "starting", types.TaskResource{Type: "lxc", VMID: i}, "tenant-a")
	}
	n := r.Notifications("", true)
	if len(n) != notificationCap {
		t.Fatalf("ring size = %d, want %d", len(n), notificationCap)
	}

	r.MarkRead([]string{n[0].ID, n[1].ID, "nonexistent"}, "", true)
	n = r.Notifications("", true)
	if !n[0].Read || !n[1].Read || n[2].Read {
		t.Fatalf("read flags = %v %v %v", n[0].Read, n[1].Read, n[2].Read)
	}
}

// TestRegistryNotificationsScopedToTenant is the H6 regression at the ring
// level: a non-admin caller sees only entries stored under its own tenant, and
// can mark-read only those — the process-global ring never leaks or lets one
// tenant touch another's entries. A caller with no active tenant sees nothing
// (fail closed), and untenanted ("") entries are admin-only.
func TestRegistryNotificationsScopedToTenant(t *testing.T) {
	r := NewRegistry()
	r.Track("UPID:x:1:vzcreate:100:", "Create container", "provisioning", types.TaskResource{Type: "lxc", VMID: 100, Name: "a-owned"}, "tenant-a")
	r.Track("UPID:x:2:vzcreate:200:", "Create container", "provisioning", types.TaskResource{Type: "lxc", VMID: 200, Name: "b-owned"}, "tenant-b")
	r.Track("UPID:x:3:vzcreate:300:", "Create container", "provisioning", types.TaskResource{Type: "lxc", VMID: 300, Name: "untenanted"}, "")

	got := r.Notifications("tenant-a", false)
	if len(got) != 1 || got[0].UPID != "UPID:x:1:vzcreate:100:" {
		t.Fatalf("scoped notifications = %+v, want only tenant-a's entry", got)
	}
	// No active tenant → nothing; the "" entry must NOT match an "" caller.
	if got := r.Notifications("", false); len(got) != 0 {
		t.Fatalf("tenantless caller sees %d entries, want 0 (fail closed)", len(got))
	}
	// The full ring (admin) has all three.
	if all := r.Notifications("", true); len(all) != 3 {
		t.Fatalf("admin sees %d, want 3", len(all))
	}
	// A non-owner cannot mark another tenant's entry read.
	var otherID string
	for _, n := range r.Notifications("", true) {
		if n.UPID == "UPID:x:2:vzcreate:200:" {
			otherID = n.ID
		}
	}
	r.MarkRead([]string{otherID}, "tenant-a", false) // tenant-a tries to flip tenant-b's
	for _, n := range r.Notifications("", true) {
		if n.ID == otherID && n.Read {
			t.Fatalf("a non-owner flipped another tenant's notification read")
		}
	}
}

// TestRegistryVMIDReuseDoesNotLeak is the M-b regression: VMIDs are
// caller-chosen and reissued after a tombstone, so entries recorded for tenant
// A on VMID 105 must never surface to tenant B after B claims the freed VMID —
// with tenant filtering alone, AND with the DropVMID tombstone cleanup.
func TestRegistryVMIDReuseDoesNotLeak(t *testing.T) {
	r := NewRegistry()

	// Tenant A works VMID 105, including a failed task with a verbatim PVE error.
	r.Track("UPID:x:10:vzstart:105:", "Start container", "starting", types.TaskResource{Type: "lxc", VMID: 105, Name: "a-secret-name"}, "tenant-a")
	r.Complete("UPID:x:10:vzstart:105:", false, "a-secret-name: storage 'ceph-a' unavailable")

	// Even BEFORE any cleanup, tenant B (different tenant, same VMID) sees nothing
	// and cannot flip A's entry — the stored tenant is the filter, not the VMID.
	aID := r.Notifications("tenant-a", false)[0].ID
	if got := r.Notifications("tenant-b", false); len(got) != 0 {
		t.Fatalf("tenant B sees %d of A's entries via reused VMID, want 0", len(got))
	}
	r.MarkRead([]string{aID}, "tenant-b", false)
	if r.Notifications("tenant-a", false)[0].Read {
		t.Fatal("tenant B flipped tenant A's notification via a reused VMID")
	}

	// A's guest is destroyed → tombstone choke-point calls DropVMID.
	r.DropVMID(105)
	if got := r.Notifications("", true); len(got) != 0 {
		t.Fatalf("after DropVMID admin still sees %d entries, want 0 (nothing carries forward)", len(got))
	}

	// Tenant B claims the freed VMID: only B's own fresh entry is visible to B;
	// tenant A sees nothing on the VMID it no longer owns.
	r.Track("UPID:x:11:vzcreate:105:", "Create container", "provisioning", types.TaskResource{Type: "lxc", VMID: 105, Name: "b-fresh"}, "tenant-b")
	if got := r.Notifications("tenant-b", false); len(got) != 1 || got[0].UPID != "UPID:x:11:vzcreate:105:" {
		t.Fatalf("tenant B after reuse = %+v, want exactly its own fresh entry", got)
	}
	if got := r.Notifications("tenant-a", false); len(got) != 0 {
		t.Fatalf("tenant A sees %d entries on a VMID it no longer owns, want 0", len(got))
	}
}
