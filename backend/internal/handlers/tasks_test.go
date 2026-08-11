package handlers_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
)

const escUPID = "UPID%3Apve01%3A000A%3A000B%3A66B0F2E1%3Aqmstart%3A101%3Aroot%40pam%21tok%3A"

func TestListTasks(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnClusterTasks: func(context.Context) ([]proxmox.TaskInfo, error) {
			return []proxmox.TaskInfo{
				{UPID: "UPID:pve01:1:1:1:qmstart:101:root@pam:", Node: "pve01", Type: "qmstart", ID: "101", User: "root@pam", StartTime: 300, EndTime: 0},
				{UPID: "UPID:pve01:2:2:2:vzcreate:200:root@pam:", Node: "pve01", Type: "vzcreate", ID: "200", User: "root@pam", StartTime: 200, EndTime: 210, ExitStatus: "OK"},
				{UPID: "UPID:pve01:3:3:3:qmdestroy:102:root@pam:", Node: "pve01", Type: "qmdestroy", ID: "102", User: "root@pam", StartTime: 100, EndTime: 110, ExitStatus: "can't lock file"},
				{UPID: "UPID:pve01:4:4:4:aptupdate::root@pam:", Node: "pve01", Type: "aptupdate", ID: "", User: "root@pam", StartTime: 50, EndTime: 60, ExitStatus: "WARNINGS: 2"},
			}, nil
		},
	}
	rec, _ := do(t, mock, http.MethodGet, "/api/tasks")
	if rec.Code != 200 {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	list := decodeBody[[]types.TaskSummary](t, rec)
	if len(list) != 4 {
		t.Fatalf("got %d tasks, want 4", len(list))
	}

	assert := func(i int, status, action string, hasRes bool, resType string) {
		t.Helper()
		s := list[i]
		if s.Status != status || s.Action != action {
			t.Errorf("task %d = status %q action %q, want %q/%q", i, s.Status, s.Action, status, action)
		}
		if hasRes != (s.Resource != nil) {
			t.Fatalf("task %d resource presence = %v, want %v", i, s.Resource != nil, hasRes)
		}
		if hasRes && s.Resource.Type != resType {
			t.Errorf("task %d resource type = %q, want %q", i, s.Resource.Type, resType)
		}
	}
	assert(0, "running", "Start virtual machine", true, "qemu")
	assert(1, "succeeded", "Create container", true, "lxc")
	assert(2, "failed", "Delete virtual machine", true, "qemu")
	assert(3, "succeeded", "Update package index", false, "") // WARNINGS counts as success

	if list[2].ExitStatus != "can't lock file" {
		t.Errorf("failed task must carry the verbatim PVE exit status, got %q", list[2].ExitStatus)
	}

	// running=true filter
	rec, _ = do(t, mock, http.MethodGet, "/api/tasks?running=true")
	if l := decodeBody[[]types.TaskSummary](t, rec); len(l) != 1 || l[0].Status != "running" {
		t.Errorf("running filter returned %d rows", len(l))
	}
	// vmid filter
	rec, _ = do(t, mock, http.MethodGet, "/api/tasks?vmid=200")
	if l := decodeBody[[]types.TaskSummary](t, rec); len(l) != 1 || l[0].Resource.VMID != 200 {
		t.Errorf("vmid filter returned wrong rows: %+v", l)
	}
}

func TestGetTaskUPIDEscaping(t *testing.T) {
	want, _ := url.PathUnescape(escUPID)
	var got proxmox.UPID
	mock := &proxmoxtest.MockClient{
		OnTaskStatus: func(_ context.Context, upid proxmox.UPID) (*proxmox.TaskInfo, error) {
			got = upid
			return &proxmox.TaskInfo{UPID: upid, Node: "pve01", Type: "qmstart", ID: "101", StartTime: 10, EndTime: 20, ExitStatus: "OK"}, nil
		},
	}
	rec, _ := do(t, mock, http.MethodGet, "/api/tasks/"+escUPID)
	if rec.Code != 200 {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	if string(got) != want {
		t.Errorf("client received UPID %q, want unescaped %q", got, want)
	}
	if s := decodeBody[types.TaskSummary](t, rec); s.Status != "succeeded" || s.EndedAt == nil {
		t.Errorf("summary = %+v, want succeeded with EndedAt", s)
	}

	rec, _ = do(t, mock, http.MethodGet, "/api/tasks/not-a-upid")
	wantErrorEnvelope(t, rec, 400, "invalid_request")
}

func TestGetTaskLog(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnTaskLog: func(_ context.Context, _ proxmox.UPID, start, limit int) ([]types.TaskLogLine, int, error) {
			if start != 40 || limit != 20 {
				t.Errorf("pagination passthrough = start %d limit %d, want 40/20", start, limit)
			}
			return []types.TaskLogLine{{N: 41, T: "line"}}, 41, nil
		},
	}
	rec, _ := do(t, mock, http.MethodGet, "/api/tasks/"+escUPID+"/log?start=40&limit=20")
	if rec.Code != 200 {
		t.Fatalf("status = %d (%s)", rec.Code, rec.Body)
	}
	if l := decodeBody[types.TaskLog](t, rec); l.Total != 41 || len(l.Lines) != 1 {
		t.Errorf("log = %+v", l)
	}
}
