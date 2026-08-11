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
	r.Track("UPID:x:1:qmstart:", "Start virtual machine", "starting", res)

	if st, upid, ok := r.ActiveFor(101); !ok || st != "starting" || upid != "UPID:x:1:qmstart:" {
		t.Fatalf("ActiveFor = %q %q %v", st, upid, ok)
	}
	if _, _, ok := r.ActiveFor(999); ok {
		t.Fatal("ActiveFor matched wrong vmid")
	}
	if len(r.Running()) != 1 {
		t.Fatalf("Running = %d, want 1", len(r.Running()))
	}

	n := r.Notifications()
	if len(n) != 1 || n[0].Kind != "prog" || n[0].Status != "running" || n[0].Title != "Start virtual machine" {
		t.Fatalf("notification = %+v", n)
	}

	tr := r.Complete("UPID:x:1:qmstart:", true, "OK")
	if tr == nil || tr.Resource.Name != "web-01" {
		t.Fatalf("Complete returned %+v", tr)
	}
	if _, _, ok := r.ActiveFor(101); ok {
		t.Fatal("overlay survived completion")
	}
	n = r.Notifications()
	if n[0].Kind != "ok" || n[0].Status != "succeeded" {
		t.Fatalf("completed notification = %+v", n[0])
	}
	if r.Complete("UPID:x:1:qmstart:", true, "OK") != nil {
		t.Fatal("double Complete returned a value")
	}
}

func TestRegistryFailureCarriesExitStatus(t *testing.T) {
	r := NewRegistry()
	r.Track("UPID:x:2:qmstop:", "Stop virtual machine", "stopping", types.TaskResource{Type: "qemu", VMID: 5, Name: "db"})
	r.Complete("UPID:x:2:qmstop:", false, "timeout waiting on systemd")

	n := r.Notifications()[0]
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
		r.Track(proxmox.UPID(fmt.Sprintf("UPID:x:%d:vzstart:", i)), "Start container", "starting", types.TaskResource{Type: "lxc", VMID: i})
	}
	n := r.Notifications()
	if len(n) != notificationCap {
		t.Fatalf("ring size = %d, want %d", len(n), notificationCap)
	}

	r.MarkRead([]string{n[0].ID, n[1].ID, "nonexistent"})
	n = r.Notifications()
	if !n[0].Read || !n[1].Read || n[2].Read {
		t.Fatalf("read flags = %v %v %v", n[0].Read, n[1].Read, n[2].Read)
	}
}
