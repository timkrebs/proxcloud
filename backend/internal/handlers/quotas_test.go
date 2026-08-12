package handlers_test

import (
	"context"
	"net/http"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
)

const (
	gib2  = int64(2) * 1024 * 1024 * 1024
	gib40 = int64(40) * 1024 * 1024 * 1024
)

func qtiptr(v int) *int       { return &v }
func qti64ptr(v int64) *int64 { return &v }

// snapshotMock returns a mock whose ClusterResources reports one active guest
// (VMID 101: 4 vCPU, 2 GiB RAM, 40 GiB disk).
func snapshotMock() *proxmoxtest.MockClient {
	return &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) {
			return []proxmox.RawResource{
				{ID: "qemu/101", Type: "qemu", VMID: 101, Name: "web", Node: "pve01", Status: "running",
					MaxCPU: 4, MaxMem: gib2, MaxDisk: gib40},
				{ID: "node/pve01", Type: "node", Node: "pve01"},
			}, nil
		},
	}
}

// TestGetTenantQuotaUsage: usage is computed from ClusterResources against the
// tenant's owned rows; remaining reflects the stored limits.
func TestGetTenantQuotaUsage(t *testing.T) {
	hh := newHarness(t, snapshotMock())
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "reader")
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	hh.fake.AddQuota("tenant", tenantA, qtiptr(8), qti64ptr(8192), qti64ptr(200), qtiptr(10))
	c := hh.cookie(t, userA)

	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/quota", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GetTenantQuota = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	q := decodeBody[types.QuotaWithUsage](t, rec)
	if q.ScopeType != "tenant" || q.ScopeID != tenantA {
		t.Fatalf("scope = %s/%s, want tenant/%s", q.ScopeType, q.ScopeID, tenantA)
	}
	if q.Usage.VCPU != 4 || q.Usage.RAMMB != 2048 || q.Usage.DiskGB != 40 || q.Usage.Count != 1 {
		t.Fatalf("usage = %+v, want 4/2048/40/1", q.Usage)
	}
	if q.Limits.MaxVCPU == nil || *q.Limits.MaxVCPU != 8 {
		t.Fatalf("limits.maxVcpu = %v, want 8", q.Limits.MaxVCPU)
	}
	if q.Remaining.VCPU != 4 || q.Remaining.RAMMB != 6144 || q.Remaining.DiskGB != 160 || q.Remaining.Count != 9 {
		t.Fatalf("remaining = %+v, want 4/6144/160/9", q.Remaining)
	}
}

// TestGetTenantQuotaUnlimited: no stored row ⇒ all limits null, usage still real.
func TestGetTenantQuotaUnlimited(t *testing.T) {
	hh := newHarness(t, snapshotMock())
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "reader")
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userA)

	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/quota", "")
	q := decodeBody[types.QuotaWithUsage](t, rec)
	if q.Limits.MaxVCPU != nil || q.Limits.MaxRAMMB != nil || q.Limits.MaxDiskGB != nil || q.Limits.MaxCount != nil {
		t.Fatalf("limits = %+v, want all nil (unlimited)", q.Limits)
	}
	if q.Usage.VCPU != 4 {
		t.Fatalf("usage.vcpu = %d, want 4", q.Usage.VCPU)
	}
	if q.Remaining.VCPU != 0 {
		t.Fatalf("remaining.vcpu = %d, want 0 (no limit ⇒ no remaining)", q.Remaining.VCPU)
	}
}

// TestGetProjectQuotaRollup: the response carries both the project scope and the
// tenant rollup so the wizard can bind on the tighter remaining.
func TestGetProjectQuotaRollup(t *testing.T) {
	hh := newHarness(t, snapshotMock())
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "reader")
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	hh.fake.AddQuota("tenant", tenantA, qtiptr(8), nil, nil, nil)
	hh.fake.AddQuota("project", projA, qtiptr(6), nil, nil, nil)
	c := hh.cookie(t, userA)

	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/projects/"+projA+"/quota", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GetProjectQuota = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	resp := decodeBody[types.ProjectQuotaResponse](t, rec)
	if resp.Project.ScopeType != "project" || resp.Project.ScopeID != projA {
		t.Fatalf("project scope = %+v", resp.Project)
	}
	if resp.Project.Limits.MaxVCPU == nil || *resp.Project.Limits.MaxVCPU != 6 || resp.Project.Usage.VCPU != 4 {
		t.Fatalf("project = %+v, want limit 6 usage 4", resp.Project)
	}
	if resp.Tenant.ScopeType != "tenant" || resp.Tenant.Limits.MaxVCPU == nil || *resp.Tenant.Limits.MaxVCPU != 8 {
		t.Fatalf("tenant rollup = %+v, want tenant limit 8", resp.Tenant)
	}
}

// TestGetProjectQuotaCrossTenant404: a projectId from another tenant is a 404
// (ResolveScope), never a leak.
func TestGetProjectQuotaCrossTenant404(t *testing.T) {
	hh := newHarness(t, snapshotMock())
	tenantA := hh.fake.AddTenant("A", "a")
	tenantB := hh.fake.AddTenant("B", "b")
	projB := hh.fake.AddProject(tenantB, "Web", "web", "pc-b-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "owner")
	c := hh.cookie(t, userA)

	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/projects/"+projB+"/quota", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant project quota = %d, want 404", rec.Code)
	}
}

// TestPutProjectQuotaAuthzAndValidation: Owner-only, and each limit must be ≤ the
// tenant limit (400 otherwise).
func TestPutProjectQuotaAuthzAndValidation(t *testing.T) {
	hh := newHarness(t, snapshotMock())
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	hh.fake.AddQuota("tenant", tenantA, qtiptr(8), nil, nil, nil)

	owner := hh.fake.AddUser("o@x.io", "Ojo", false)
	hh.fake.AddMembership(owner, "tenant", tenantA, "owner")
	contrib := hh.fake.AddUser("c@x.io", "Cid", false)
	hh.fake.AddMembership(contrib, "tenant", tenantA, "contributor")
	cOwner := hh.cookie(t, owner)
	cContrib := hh.cookie(t, contrib)

	// Contributor is denied (403).
	if rec := hh.req(t, cContrib, http.MethodPut, "/api/tenants/"+tenantA+"/projects/"+projA+"/quota", `{"maxVcpu":4}`); rec.Code != http.StatusForbidden {
		t.Fatalf("contributor PUT quota = %d, want 403", rec.Code)
	}
	// Owner within the tenant cap → 200.
	rec := hh.req(t, cOwner, http.MethodPut, "/api/tenants/"+tenantA+"/projects/"+projA+"/quota", `{"maxVcpu":4}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("owner PUT quota = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	q := decodeBody[types.QuotaWithUsage](t, rec)
	if q.ScopeType != "project" || q.Limits.MaxVCPU == nil || *q.Limits.MaxVCPU != 4 {
		t.Fatalf("put result = %+v, want project maxVcpu 4", q)
	}
	// Owner exceeding the tenant cap → 400.
	if rec := hh.req(t, cOwner, http.MethodPut, "/api/tenants/"+tenantA+"/projects/"+projA+"/quota", `{"maxVcpu":16}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("owner PUT over tenant cap = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	// Negative limit → 400.
	if rec := hh.req(t, cOwner, http.MethodPut, "/api/tenants/"+tenantA+"/projects/"+projA+"/quota", `{"maxVcpu":-1}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("owner PUT negative = %d, want 400", rec.Code)
	}
}

// TestPutProjectQuotaTenantUnlimited: with no tenant quota (unlimited), any
// project limit is accepted.
func TestPutProjectQuotaTenantUnlimited(t *testing.T) {
	hh := newHarness(t, snapshotMock())
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	owner := hh.fake.AddUser("o@x.io", "Ojo", false)
	hh.fake.AddMembership(owner, "tenant", tenantA, "owner")
	c := hh.cookie(t, owner)

	if rec := hh.req(t, c, http.MethodPut, "/api/tenants/"+tenantA+"/projects/"+projA+"/quota", `{"maxVcpu":999}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT under unlimited tenant = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestAdminTenantQuota: platform-admin-only; non-admin blocked; missing tenant 404.
func TestAdminTenantQuota(t *testing.T) {
	hh := newHarness(t, snapshotMock())
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)

	owner := hh.fake.AddUser("o@x.io", "Ojo", false) // NOT platform-admin
	hh.fake.AddMembership(owner, "tenant", tenantA, "owner")
	admin := hh.fake.AddUser("root@x.io", "Root", true)
	cOwner := hh.cookie(t, owner)
	cAdmin := hh.cookie(t, admin)

	// A tenant Owner may not reach the admin tenant-quota surface (403).
	if rec := hh.req(t, cOwner, http.MethodGet, "/api/admin/tenants/"+tenantA+"/quota", ""); rec.Code != http.StatusForbidden {
		t.Fatalf("owner GET admin quota = %d, want 403", rec.Code)
	}
	if rec := hh.req(t, cOwner, http.MethodPut, "/api/admin/tenants/"+tenantA+"/quota", `{"maxVcpu":8}`); rec.Code != http.StatusForbidden {
		t.Fatalf("owner PUT admin quota = %d, want 403", rec.Code)
	}

	// Admin sets the tenant cap, then reads it back with live usage.
	if rec := hh.req(t, cAdmin, http.MethodPut, "/api/admin/tenants/"+tenantA+"/quota", `{"maxVcpu":8}`); rec.Code != http.StatusOK {
		t.Fatalf("admin PUT quota = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	rec := hh.req(t, cAdmin, http.MethodGet, "/api/admin/tenants/"+tenantA+"/quota", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("admin GET quota = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	q := decodeBody[types.QuotaWithUsage](t, rec)
	if q.Limits.MaxVCPU == nil || *q.Limits.MaxVCPU != 8 || q.Usage.VCPU != 4 {
		t.Fatalf("admin quota = %+v, want limit 8 usage 4", q)
	}

	// A nonexistent tenant → 404 (no fabricated all-zero quota).
	if rec := hh.req(t, cAdmin, http.MethodGet, "/api/admin/tenants/00000000-0000-0000-0000-000000000000/quota", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("admin GET missing tenant quota = %d, want 404", rec.Code)
	}
}
