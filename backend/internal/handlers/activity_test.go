package handlers_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

var activityBase = time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)

// seedActivity wires a tenant with one owned guest, two audit rows (a
// user-attributed guest.create and a system reservation.reclaimed), a
// cross-tenant audit row that must never appear, and a ClusterTasks overlay with
// one task targeting the owned guest.
func seedActivity(t *testing.T) (*harness, *http.Cookie, string) {
	t.Helper()
	mock := &proxmoxtest.MockClient{
		OnClusterTasks: func(context.Context) ([]proxmox.TaskInfo, error) {
			return []proxmox.TaskInfo{{
				UPID: "UPID:pve01:1:2:3:qmstart:101:root@pam:", Node: "pve01", Type: "qmstart",
				ID: "101", User: "root@pam",
				StartTime: activityBase.Add(1 * time.Minute).Unix(),
				EndTime:   activityBase.Add(90 * time.Second).Unix(), ExitStatus: "OK",
			}}, nil
		},
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	tenantB := hh.fake.AddTenant("B", "b")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "reader")
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)

	// audit1: user-attributed guest.create (oldest).
	hh.fake.AddAuditEntry(store.AuditEntry{
		TS: activityBase, ActorUserID: strptr(userA), TenantID: strptr(tenantA), ProjectID: strptr(projA),
		Action: "guest.create", TargetType: strptr("guest"), TargetID: strptr("101"), Outcome: "success",
	})
	// audit2: system reservation.reclaimed (newest), nil actor.
	hh.fake.AddAuditEntry(store.AuditEntry{
		TS: activityBase.Add(2 * time.Minute), ActorUserID: nil, TenantID: strptr(tenantA),
		Action: "reservation.reclaimed", Outcome: "success",
	})
	// cross-tenant row that must be filtered out.
	hh.fake.AddAuditEntry(store.AuditEntry{
		TS: activityBase.Add(90 * time.Second), TenantID: strptr(tenantB), Action: "guest.delete", Outcome: "success",
	})
	return hh, hh.cookie(t, userA), tenantA
}

// TestActivityMergeSortEnrich: audit spine + task overlay merged, sorted ts DESC,
// actor + project names resolved, cross-tenant rows excluded.
func TestActivityMergeSortEnrich(t *testing.T) {
	hh, c, tenantA := seedActivity(t)

	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/activity", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GetActivity = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	page := decodeBody[types.ActivityPage](t, rec)
	if len(page.Entries) != 3 {
		t.Fatalf("entries = %d, want 3 (2 audit + 1 task, cross-tenant excluded): %+v", len(page.Entries), page.Entries)
	}
	// Sorted ts DESC: reservation.reclaimed (base+2m), task (base+1m), guest.create (base).
	if page.Entries[0].Source != "audit" || page.Entries[0].Action != "reservation.reclaimed" || page.Entries[0].Actor != "system" {
		t.Fatalf("entries[0] = %+v, want system reservation.reclaimed", page.Entries[0])
	}
	task := page.Entries[1]
	if task.Source != "task" || task.Action != "Start virtual machine" || task.TargetID != "101" ||
		task.Outcome != "succeeded" || task.ProjectName != "Web" || task.UPID == "" {
		t.Fatalf("entries[1] = %+v, want enriched succeeded task on Web", task)
	}
	create := page.Entries[2]
	if create.Source != "audit" || create.Action != "guest.create" || create.Actor != "Ada" ||
		create.ProjectName != "Web" || create.Outcome != "success" {
		t.Fatalf("entries[2] = %+v, want Ada guest.create on Web", create)
	}
	// Full page not filled (2 audit rows < limit) → no older audit rows.
	if page.NextBefore != nil {
		t.Fatalf("NextBefore = %v, want nil", page.NextBefore)
	}
}

// TestActivitySourceFilter: source=audit drops the task overlay; source=task
// drops the audit rows.
func TestActivitySourceFilter(t *testing.T) {
	hh, c, tenantA := seedActivity(t)

	recA := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/activity?source=audit", "")
	pageA := decodeBody[types.ActivityPage](t, recA)
	if len(pageA.Entries) != 2 {
		t.Fatalf("source=audit entries = %d, want 2", len(pageA.Entries))
	}
	for _, e := range pageA.Entries {
		if e.Source != "audit" {
			t.Fatalf("source=audit returned a %q entry", e.Source)
		}
	}

	recT := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/activity?source=task", "")
	pageT := decodeBody[types.ActivityPage](t, recT)
	if len(pageT.Entries) != 1 || pageT.Entries[0].Source != "task" {
		t.Fatalf("source=task entries = %+v, want 1 task", pageT.Entries)
	}
}

// TestActivityKeysetPagination: limit fills the page from the audit spine and
// NextBefore advances the cursor to the older rows.
func TestActivityKeysetPagination(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnClusterTasks: func(context.Context) ([]proxmox.TaskInfo, error) { return nil, nil },
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "reader")
	for i := 0; i < 3; i++ {
		hh.fake.AddAuditEntry(store.AuditEntry{
			TS: activityBase.Add(time.Duration(i) * time.Minute), TenantID: strptr(tenantA),
			Action: "project.create", Outcome: "success",
		})
	}
	c := hh.cookie(t, userA)

	// Page 1: newest two, NextBefore = ts of the older of the two.
	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/activity?limit=2", "")
	page := decodeBody[types.ActivityPage](t, rec)
	if len(page.Entries) != 2 {
		t.Fatalf("page1 entries = %d, want 2", len(page.Entries))
	}
	if !page.Entries[0].TS.After(page.Entries[1].TS) {
		t.Fatalf("page1 not sorted DESC: %v then %v", page.Entries[0].TS, page.Entries[1].TS)
	}
	if page.NextBefore == nil {
		t.Fatalf("page1 NextBefore = nil, want the oldest page ts")
	}

	// Page 2: everything strictly older than NextBefore → the remaining row.
	before := page.NextBefore.UTC().Format(time.RFC3339Nano)
	rec2 := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/activity?limit=2&before="+url.QueryEscape(before), "")
	page2 := decodeBody[types.ActivityPage](t, rec2)
	if len(page2.Entries) != 1 {
		t.Fatalf("page2 entries = %d, want 1 (the oldest row)", len(page2.Entries))
	}
	if page2.NextBefore != nil {
		t.Fatalf("page2 NextBefore = %v, want nil (last page)", page2.NextBefore)
	}
}

// TestActivityBadBefore: a non-RFC3339 before cursor is a 400.
func TestActivityBadBefore(t *testing.T) {
	hh, c, tenantA := seedActivity(t)
	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/activity?before=not-a-time", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad before = %d, want 400", rec.Code)
	}
}
