package handlers_test

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// --- CreateTenantAdmin: pool-first + atomic (CR-2) ---

// Happy path: the tenant, its default project, and the pool all come into being.
func TestCreateTenantAdminCreatesTenantProjectAndPool(t *testing.T) {
	var (
		mu    sync.Mutex
		pools []string
	)
	mock := &proxmoxtest.MockClient{
		OnCreatePool: func(_ context.Context, poolID, _ string) error {
			mu.Lock()
			pools = append(pools, poolID)
			mu.Unlock()
			return nil
		},
	}
	hh := newHarness(t, mock)
	admin := hh.fake.AddUser("root@x.io", "Root", true)
	c := hh.cookie(t, admin)

	rec := hh.req(t, c, http.MethodPost, "/api/admin/tenants", `{"name":"Acme Corp"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tenant = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	mu.Lock()
	gotPools := append([]string(nil), pools...)
	mu.Unlock()
	if len(gotPools) != 1 || gotPools[0] != "pc-acme-corp-default" {
		t.Fatalf("ensured pools = %v, want [pc-acme-corp-default]", gotPools)
	}
	ten, err := hh.fake.GetTenantBySlug(context.Background(), "acme-corp")
	if err != nil {
		t.Fatalf("tenant not committed: %v", err)
	}
	projs, _ := hh.fake.ListProjectsByTenant(context.Background(), ten.ID)
	if len(projs) != 1 || projs[0].Slug != "default" || projs[0].PoolID != "pc-acme-corp-default" {
		t.Fatalf("default project = %+v, want one default/pc-acme-corp-default", projs)
	}
}

// Pool-first: an EnsureProjectPool failure surfaces the verbatim PVE error AND
// leaves no tenant/project rows (a clean retry — no orphaned tenant, F4).
func TestCreateTenantAdminPoolFailureLeavesNoRows(t *testing.T) {
	pveErr := &types.APIError{Code: "proxmox_error", Message: "Pool.Allocate missing", Status: http.StatusBadGateway}
	mock := &proxmoxtest.MockClient{
		OnCreatePool: func(context.Context, string, string) error { return pveErr },
	}
	hh := newHarness(t, mock)
	admin := hh.fake.AddUser("root@x.io", "Root", true)
	c := hh.cookie(t, admin)

	rec := hh.req(t, c, http.MethodPost, "/api/admin/tenants", `{"name":"Acme"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("pool failure = %d, want 502 (honest PVE error) — body %s", rec.Code, rec.Body.String())
	}
	if _, err := hh.fake.GetTenantBySlug(context.Background(), "acme"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("tenant row leaked after pool failure: err=%v (must create NO rows so retry is clean)", err)
	}
}

// Atomic: a project-write conflict inside the tx rolls the tenant back (409, no
// orphan) rather than committing a tenant with no usable project.
func TestCreateTenantAdminRollsBackTenantOnProjectConflict(t *testing.T) {
	mock := &proxmoxtest.MockClient{OnCreatePool: func(context.Context, string, string) error { return nil }}
	hh := newHarness(t, mock)
	// A squatter project already holds the pool id the new tenant's default
	// project would derive — CreateProject will conflict (global pool_id unique).
	other := hh.fake.AddTenant("Other", "other")
	hh.fake.AddProject(other, "Squatter", "squat", "pc-acme-default")
	admin := hh.fake.AddUser("root@x.io", "Root", true)
	c := hh.cookie(t, admin)

	rec := hh.req(t, c, http.MethodPost, "/api/admin/tenants", `{"name":"Acme"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("colliding pool id = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if _, err := hh.fake.GetTenantBySlug(context.Background(), "acme"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("tenant orphaned after project conflict: err=%v (WithTx must roll back)", err)
	}
}

// A duplicate slug is rejected 409 by the pre-check WITHOUT provisioning a pool
// for a request that will be refused.
func TestCreateTenantAdminDuplicateSlugSkipsPool(t *testing.T) {
	var poolCalls int32
	mock := &proxmoxtest.MockClient{
		OnCreatePool: func(context.Context, string, string) error { atomic.AddInt32(&poolCalls, 1); return nil },
	}
	hh := newHarness(t, mock)
	hh.fake.AddTenant("Acme", "acme") // already exists
	admin := hh.fake.AddUser("root@x.io", "Root", true)
	c := hh.cookie(t, admin)

	rec := hh.req(t, c, http.MethodPost, "/api/admin/tenants", `{"name":"Acme"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate tenant = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if n := atomic.LoadInt32(&poolCalls); n != 0 {
		t.Fatalf("pool provisioned for a duplicate tenant (calls=%d); the pre-check must short-circuit", n)
	}
}

// --- CreateProject: pool-first + 409 (CR-5, L1) ---

// A cross-tenant pool-id collision (slugs-with-hyphens rendering the same
// pc-<tenant>-<project> string) is rejected 409 by the global pool_id unique,
// not silently bound to another tenant's pool.
func TestCreateProjectCrossTenantPoolCollisionConflict(t *testing.T) {
	mock := &proxmoxtest.MockClient{OnCreatePool: func(context.Context, string, string) error { return nil }}
	hh := newHarness(t, mock)
	// tenant "a-b" already owns pool pc-a-b-c via project slug "c".
	tenAB := hh.fake.AddTenant("A B", "a-b")
	hh.fake.AddProject(tenAB, "C", "c", "pc-a-b-c")
	// tenant "a" owner creates project "b-c" → derives the SAME pool pc-a-b-c.
	tenA := hh.fake.AddTenant("A", "a")
	owner := hh.fake.AddUser("o@x.io", "Ojo", false)
	hh.fake.AddMembership(owner, "tenant", tenA, "owner")
	c := hh.cookie(t, owner)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenA+"/projects", `{"name":"b-c"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("cross-tenant pool collision = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

// --- RenameProject: defensive cross-tenant 404 (CR-5 nit) ---

func TestRenameProjectCrossTenant404(t *testing.T) {
	hh := newHarness(t, &proxmoxtest.MockClient{})
	tenA := hh.fake.AddTenant("A", "a")
	tenB := hh.fake.AddTenant("B", "b")
	projA := hh.fake.AddProject(tenA, "Web", "web", "pc-a-web")
	owner := hh.fake.AddUser("o@x.io", "Ojo", false)
	hh.fake.AddMembership(owner, "tenant", tenB, "owner")
	c := hh.cookie(t, owner)

	rec := hh.req(t, c, http.MethodPatch, "/api/tenants/"+tenB+"/projects/"+projA, `{"name":"Renamed"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant rename = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if p, _ := hh.fake.GetProjectByID(context.Background(), projA); p.Name != "Web" {
		t.Fatalf("foreign project renamed to %q, want Web (no cross-tenant write)", p.Name)
	}
}

// --- ListMembers: batched project-scope query (CR-4) ---

func TestListMembersBatchedAcrossScopes(t *testing.T) {
	hh := newHarness(t, &proxmoxtest.MockClient{})
	tenA := hh.fake.AddTenant("A", "a")
	p1 := hh.fake.AddProject(tenA, "P1", "p1", "pc-a-p1")
	p2 := hh.fake.AddProject(tenA, "P2", "p2", "pc-a-p2")
	owner := hh.fake.AddUser("o@x.io", "Ojo", false)
	u1 := hh.fake.AddUser("u1@x.io", "One", false)
	u2 := hh.fake.AddUser("u2@x.io", "Two", false)
	hh.fake.AddMembership(owner, "tenant", tenA, "owner")
	hh.fake.AddMembership(u1, "project", p1, "contributor")
	hh.fake.AddMembership(u2, "project", p2, "reader")
	c := hh.cookie(t, owner)

	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenA+"/members", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list members = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	got := decodeBody[[]types.Member](t, rec)
	if len(got) != 3 {
		t.Fatalf("members = %d rows, want 3 (tenant owner + both project grants) — %+v", len(got), got)
	}
	byUser := map[string]types.Member{}
	for _, m := range got {
		byUser[m.UserID] = m
	}
	if m := byUser[u1]; m.ScopeType != "project" || m.ScopeID != p1 || m.Role != "contributor" || m.DisplayName != "One" {
		t.Fatalf("u1 member = %+v, want project/p1/contributor/One", m)
	}
	if m := byUser[u2]; m.ScopeType != "project" || m.ScopeID != p2 || m.Role != "reader" {
		t.Fatalf("u2 member = %+v, want project/p2/reader", m)
	}
	if m := byUser[owner]; m.ScopeType != "tenant" || m.ScopeID != tenA {
		t.Fatalf("owner member = %+v, want tenant/%s", m, tenA)
	}
}
