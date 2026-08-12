package handlers_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// allAuditRows reads every audit row (tenant-filter-agnostic) so create intents
// (whose tenant_id is nil until the tenant exists) are visible.
func allAuditRows(t *testing.T, hh *harness) []store.AuditEntry {
	t.Helper()
	return hh.fake.AllAudit()
}

// --- CR-1 / LOW-1: PUT /api/admin/tenants/{id}/quota is audited ---

// A successful admin quota change writes exactly one tenant.quota.update row,
// actor = the admin, outcome success, tenant-scoped so it shows under the tenant
// activity log.
func TestPutAdminTenantQuotaWritesAudit(t *testing.T) {
	hh := newHarness(t, snapshotMock())
	tenantA := hh.fake.AddTenant("A", "a")
	admin := hh.fake.AddUser("root@x.io", "Root", true)
	cAdmin := hh.cookie(t, admin)

	rec := hh.req(t, cAdmin, http.MethodPut, "/api/admin/tenants/"+tenantA+"/quota", `{"maxVcpu":8}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin PUT quota = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	rows, err := hh.fake.ListAudit(context.Background(), store.AuditQuery{TenantID: tenantA, Limit: 50})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", len(rows))
	}
	e := rows[0]
	if e.Action != "tenant.quota.update" {
		t.Fatalf("audit action = %q, want tenant.quota.update", e.Action)
	}
	if e.Outcome != "success" {
		t.Fatalf("audit outcome = %q, want success", e.Outcome)
	}
	if e.ActorUserID == nil || *e.ActorUserID != admin {
		t.Fatalf("audit actor = %v, want admin %q", e.ActorUserID, admin)
	}
	if e.TargetType == nil || *e.TargetType != "tenant" || e.TargetID == nil || *e.TargetID != tenantA {
		t.Fatalf("audit target = %v/%v, want tenant/%s", e.TargetType, e.TargetID, tenantA)
	}
	if e.Detail == nil || !bytes.Contains(e.Detail, []byte(`"status":200`)) {
		t.Fatalf("audit detail = %s, want status 200", e.Detail)
	}
}

// Fail-closed: a forced InsertAuditIntent failure is a 500 and the quota is NOT
// changed (the mutation never runs, no audit row is left).
func TestPutAdminTenantQuotaAuditIntentFailClosed(t *testing.T) {
	hh := newHarness(t, snapshotMock())
	tenantA := hh.fake.AddTenant("A", "a")
	admin := hh.fake.AddUser("root@x.io", "Root", true)
	cAdmin := hh.cookie(t, admin)
	hh.fake.FailOn("InsertAuditIntent", errors.New("audit db unavailable"))

	rec := hh.req(t, cAdmin, http.MethodPut, "/api/admin/tenants/"+tenantA+"/quota", `{"maxVcpu":8}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("intent-failure PUT = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "internal" {
		t.Fatalf("error code = %q, want internal", env.Error.Code)
	}
	// The quota was never written (mutation refused).
	if _, err := hh.fake.GetQuota(context.Background(), "tenant", tenantA); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("quota changed despite fail-closed intent: err=%v, want ErrNotFound", err)
	}
	// No audit row exists (the intent insert failed).
	if rows := allAuditRows(t, hh); len(rows) != 0 {
		t.Fatalf("audit rows after failed intent = %d, want 0", len(rows))
	}
}

// --- CR-1 / LOW-1: POST /api/admin/tenants is audited ---

func TestCreateTenantAdminWritesAudit(t *testing.T) {
	mock := &proxmoxtest.MockClient{OnCreatePool: func(context.Context, string, string) error { return nil }}
	hh := newHarness(t, mock)
	admin := hh.fake.AddUser("root@x.io", "Root", true)
	cAdmin := hh.cookie(t, admin)

	rec := hh.req(t, cAdmin, http.MethodPost, "/api/admin/tenants", `{"name":"Acme Corp"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tenant = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	rows := allAuditRows(t, hh)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", len(rows))
	}
	e := rows[0]
	if e.Action != "tenant.create" {
		t.Fatalf("audit action = %q, want tenant.create", e.Action)
	}
	if e.Outcome != "success" {
		t.Fatalf("audit outcome = %q, want success", e.Outcome)
	}
	if e.ActorUserID == nil || *e.ActorUserID != admin {
		t.Fatalf("audit actor = %v, want admin %q", e.ActorUserID, admin)
	}
	if e.TargetType == nil || *e.TargetType != "tenant" {
		t.Fatalf("audit target_type = %v, want tenant", e.TargetType)
	}
	// The created tenant is identified by its unique slug in the detail (the
	// tenant_id column can't be set on a pre-create intent).
	if e.Detail == nil || !bytes.Contains(e.Detail, []byte(`"slug":"acme-corp"`)) {
		t.Fatalf("audit detail = %s, want slug acme-corp", e.Detail)
	}
}

// A duplicate-name create still leaves an audited trail (outcome denied), not a
// silent gap.
func TestCreateTenantAdminDuplicateAudited(t *testing.T) {
	mock := &proxmoxtest.MockClient{OnCreatePool: func(context.Context, string, string) error { return nil }}
	hh := newHarness(t, mock)
	// Seed a colliding project pool so CreateProject conflicts inside the tx after
	// the duplicate-slug pre-check is bypassed (fresh slug, colliding pool id).
	other := hh.fake.AddTenant("Other", "other")
	hh.fake.AddProject(other, "Squatter", "squat", "pc-acme-default")
	admin := hh.fake.AddUser("root@x.io", "Root", true)
	cAdmin := hh.cookie(t, admin)

	rec := hh.req(t, cAdmin, http.MethodPost, "/api/admin/tenants", `{"name":"Acme"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("colliding create = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	rows := allAuditRows(t, hh)
	if len(rows) != 1 || rows[0].Action != "tenant.create" || rows[0].Outcome != "denied" {
		t.Fatalf("audit rows = %+v, want a single tenant.create/denied row", rows)
	}
}

// --- CR-3 / LOW-3: a clone whose source is absent from the snapshot → 400 ---

// The clone source is owned in the tenant (passes the IDOR check) but missing
// from the live ClusterResources snapshot (stale/incomplete read). The reservation
// must NOT proceed with a zero delta — it is rejected 400, no reservation is made,
// and no PVE mutation (pool/create) runs.
func TestCloneSourceMissingFromSnapshotRejected(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		// Snapshot has NO guests → the clone source (VMID 101) is absent.
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) {
			return []proxmox.RawResource{}, nil
		},
		OnCreatePool: func(context.Context, string, string) error {
			t.Fatal("EnsureProjectPool must not run for a rejected clone")
			return nil
		},
		OnCloneGuest: func(context.Context, proxmox.GuestRef, int, string, string, bool, string) (proxmox.UPID, error) {
			t.Fatal("clone must not reach PVE")
			return "", nil
		},
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	// Clone source owned in THIS tenant → ResolveOwnership passes; but it is not in
	// the snapshot above.
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userA)

	body := `{"type":"qemu","name":"clone-x","node":"pve01","vmid":305,"projectId":"` + projA + `",
		"source":{"mode":"clone","cloneVmid":101,"cloneMode":"full"},
		"cores":1,"memoryMb":512}`
	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/guests", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("clone with source absent from snapshot = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "invalid_request" {
		t.Fatalf("error code = %q, want invalid_request", env.Error.Code)
	}
	// No VMID was reserved for the rejected clone (no zero-delta reservation leaked).
	if s := hh.fake.OwnershipStatus(305); s != "" {
		t.Fatalf("a reservation leaked for the rejected clone: status %q", s)
	}
}
