package handlers_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
	"github.com/timkrebs9/proxcloud/backend/internal/deploy"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

// harness wires the REAL production router (httpserver.New with the authz chain
// and the three surface mounts) over an in-memory store + mocked Proxmox, so the
// tenancy tests exercise Authenticate → ResolveTenant → ResolveScope → Enforce
// end-to-end — not a hand-rolled copy.
type harness struct {
	h        http.Handler
	fake     *storetest.Fake
	mock     *proxmoxtest.MockClient
	engine   *deploy.Engine
	registry *tasks.Registry
	sessions *auth.Sessions
	mailer   *captureMailer
}

func newHarness(t *testing.T, mock *proxmoxtest.MockClient) *harness {
	return newHarnessLog(t, mock, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// newHarnessLog is newHarness with an injectable logger, so audit tests can
// assert the middleware's loud-but-not-fatal FinalizeAudit failure log.
func newHarnessLog(t *testing.T, mock *proxmoxtest.MockClient, log *slog.Logger) *harness {
	t.Helper()
	fake := storetest.New()
	sessions := auth.NewSessions(fake, false, time.Hour, 24*time.Hour)
	authH := &auth.Handler{Sessions: sessions, Store: fake, Hasher: auth.NewHasher(), Log: log}
	mw := &authz.Middleware{Store: fake, Log: log}
	reg := tasks.NewRegistry()
	broker := events.NewBroker()
	engine := deploy.NewEngine(mock, reg, broker, log)
	engine.Finalize = func(ctx context.Context, id, upid string) error { return fake.FinalizeOwnership(ctx, id, upid) }
	engine.Release = func(ctx context.Context, id string) error { return fake.ReleaseOwnership(ctx, id) }
	mailer := &captureMailer{}
	api := &handlers.Deps{PVE: mock, Log: log, Store: fake, Authz: mw, Deploy: engine, Registry: reg, Broker: broker,
		Mailer: mailer, FrontendOrigin: "https://portal.test", InvitationTTL: 72 * time.Hour}
	h := httpserver.New(httpserver.Deps{
		Cfg:     &config.Config{},
		Log:     log,
		Auth:    authH,
		Authz:   mw,
		Account: api.MountAccount,
		Admin:   api.MountAdmin,
		Tenant:  api.MountTenant,
	})
	return &harness{h: h, fake: fake, mock: mock, engine: engine, registry: reg, sessions: sessions, mailer: mailer}
}

// cookie issues a live session for userID and returns its cookie.
func (hh *harness) cookie(t *testing.T, userID string) *http.Cookie {
	t.Helper()
	c, err := hh.sessions.Issue(context.Background(), userID, httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("issue session: %v", err)
	}
	return c
}

// req performs one authenticated request and returns the recorder.
func (hh *harness) req(t *testing.T, c *http.Cookie, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	hh.h.ServeHTTP(rec, r)
	return rec
}

func strptr(s string) *string { return &s }

// --- ResolveTenant: non-member → 404 ---

func TestResolveTenantNonMember404(t *testing.T) {
	hh := newHarness(t, &proxmoxtest.MockClient{})
	tenantA := hh.fake.AddTenant("A", "a")
	tenantB := hh.fake.AddTenant("B", "b")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "reader")
	c := hh.cookie(t, userA)

	// Member of A → 200 on A's summary (needs the tenant row present).
	hh.fake.AddProject(tenantA, "Default", "default", "pc-a-default")
	if rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/summary", ""); rec.Code != http.StatusOK {
		t.Fatalf("own tenant summary = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	// Not a member of B → 404 (no existence leak), even though B exists.
	if rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantB+"/summary", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("non-member tenant summary = %d, want 404", rec.Code)
	}
	// A tenant that does not exist → also 404 (indistinguishable).
	if rec := hh.req(t, c, http.MethodGet, "/api/tenants/does-not-exist/summary", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing tenant summary = %d, want 404", rec.Code)
	}
}

// --- IDOR: cross-tenant VMID → 404 (never 403) across the full guest matrix ---

func TestIDORCrossTenantVMID404Matrix(t *testing.T) {
	hh := newHarness(t, &proxmoxtest.MockClient{})
	tenantA := hh.fake.AddTenant("A", "a")
	tenantB := hh.fake.AddTenant("B", "b")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	hh.fake.AddProject(tenantB, "Web", "web", "pc-b-web")
	userB := hh.fake.AddUser("b@x.io", "Bee", false)
	hh.fake.AddMembership(userB, "tenant", tenantB, "owner") // Owner: role never blocks
	// VMID 101 is owned by tenant A — a foreign resource to userB.
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userB)

	base := "/api/tenants/" + tenantB + "/guests/pve01/qemu/101"
	matrix := []struct {
		method, suffix, body string
	}{
		{http.MethodGet, "", ""},
		{http.MethodPatch, "/config", "{}"},
		{http.MethodGet, "/metrics", ""},
		{http.MethodGet, "/interfaces", ""},
		{http.MethodPost, "/resize", "{}"},
		{http.MethodGet, "/snapshots", ""},
		{http.MethodPost, "/snapshots", "{}"},
		{http.MethodPost, "/snapshots/snap1/rollback", ""},
		{http.MethodDelete, "/snapshots/snap1", ""},
		{http.MethodGet, "/firewall", ""},
		{http.MethodPut, "/firewall/options", "{}"},
		{http.MethodGet, "/acl", ""},
		{http.MethodPost, "/start", ""}, // {action}
		{http.MethodDelete, "", "{}"},   // delete guest
		{http.MethodPost, "/console", "{}"},
	}
	for _, m := range matrix {
		rec := hh.req(t, c, m.method, base+m.suffix, m.body)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 (never 403) — body %s", m.method, base+m.suffix, rec.Code, rec.Body.String())
		}
	}

	// tasks/{upid} + /log whose UPID targets VMID 101 → 404.
	upid := url.PathEscape("UPID:pve01:0001:0002:0003:qmstop:101:root@pam:")
	if rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantB+"/tasks/"+upid, ""); rec.Code != http.StatusNotFound {
		t.Errorf("tasks/{upid} cross-tenant = %d, want 404", rec.Code)
	}
	if rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantB+"/tasks/"+upid+"/log", ""); rec.Code != http.StatusNotFound {
		t.Errorf("tasks/{upid}/log cross-tenant = %d, want 404", rec.Code)
	}
}

// --- IDOR: clone source owned by another tenant → 404 ---

func TestIDORCloneSourceCrossTenant404(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnCreatePool: func(context.Context, string, string) error { return nil },
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	tenantB := hh.fake.AddTenant("B", "b")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	projB := hh.fake.AddProject(tenantB, "Web", "web", "pc-b-web")
	userB := hh.fake.AddUser("b@x.io", "Bee", false)
	hh.fake.AddMembership(userB, "tenant", tenantB, "contributor")
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", nil) // template owned by A
	c := hh.cookie(t, userB)

	body := `{"type":"qemu","name":"clone-1","node":"pve01","vmid":300,"projectId":"` + projB + `",
		"source":{"mode":"clone","cloneVmid":101,"cloneMode":"full"},
		"cores":1,"memoryMb":512}`
	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantB+"/guests", body)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("clone from foreign template = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	// No VMID was reserved for the rejected clone.
	if s := hh.fake.OwnershipStatus(300); s != "" {
		t.Fatalf("a reservation leaked for a rejected clone: status %q", s)
	}
}

// --- IDOR: deployment whose VMID is not tenant-owned → 404 ---

func TestIDORDeploymentCrossTenant404(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnCreateLXC: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			return "", &types.APIError{Code: "proxmox_error", Message: "boom", Status: 502}
		},
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	tenantB := hh.fake.AddTenant("B", "b")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	hh.fake.AddProject(tenantB, "Web", "web", "pc-b-web")
	userB := hh.fake.AddUser("b@x.io", "Bee", false)
	hh.fake.AddMembership(userB, "tenant", tenantB, "owner")
	hh.fake.AddOwnership(tenantA, projA, 101, "lxc", "pve01", "active", nil)

	// Register a deployment for VMID 101 directly on the engine (owned by A).
	dep, err := hh.engine.Submit(&types.CreateGuestRequest{
		Type: "lxc", Name: "x", Node: "pve01", VMID: 101, Cores: 1, MemoryMB: 512,
		Source:  types.CreateSource{Mode: "vztmpl", VztmplVolID: "local:vztmpl/x.tar.gz"},
		DiskGB:  8,
		Storage: "local", Bridge: "vmbr0",
	}, deploy.CreateContext{})
	if err != nil {
		t.Fatalf("seed deployment: %v", err)
	}

	c := hh.cookie(t, userB)
	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantB+"/deployments/"+dep.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("deployment for foreign VMID = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// --- effective role = max(tenant, project): adds, never subtracts ---

func TestEffectiveRoleAddNeverSubtract(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnGuestAction: func(context.Context, proxmox.GuestRef, string) (proxmox.UPID, error) {
			return "UPID:pve01:1:2:3:qmstart:0:u@pam:", nil
		},
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return nil, nil },
		OnCreatePool:       func(context.Context, string, string) error { return nil },
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projX := hh.fake.AddProject(tenantA, "X", "x", "pc-a-x")
	projY := hh.fake.AddProject(tenantA, "Y", "y", "pc-a-y")

	// userR: tenant Reader + project Contributor on projX only.
	userR := hh.fake.AddUser("r@x.io", "Ren", false)
	hh.fake.AddMembership(userR, "tenant", tenantA, "reader")
	hh.fake.AddMembership(userR, "project", projX, "contributor")
	// userC: tenant Contributor, no project role.
	userC := hh.fake.AddUser("c@x.io", "Cid", false)
	hh.fake.AddMembership(userC, "tenant", tenantA, "contributor")

	// Guest 101 in projX; guest 102 in projY.
	hh.fake.AddOwnership(tenantA, projX, 101, "qemu", "pve01", "active", nil)
	hh.fake.AddOwnership(tenantA, projY, 102, "qemu", "pve01", "active", nil)

	cR := hh.cookie(t, userR)
	cC := hh.cookie(t, userC)

	tests := []struct {
		name           string
		cookie         *http.Cookie
		method, target string
		body           string
		want           int
	}{
		// Project Contributor ADDS on projX → can mutate projX's guest.
		{"reader+projX-contrib mutates projX guest", cR, http.MethodPost, "/api/tenants/" + tenantA + "/guests/pve01/qemu/101/start", "", http.StatusAccepted},
		// …but not projY's guest (no project role there) → tenant reader only → 403.
		{"reader+projX-contrib cannot mutate projY guest", cR, http.MethodPost, "/api/tenants/" + tenantA + "/guests/pve01/qemu/102/start", "", http.StatusForbidden},
		// Tenant Reader can still READ projY's guest.
		{"reader reads projY guest", cR, http.MethodGet, "/api/tenants/" + tenantA + "/guests/pve01/qemu/102/snapshots", "", http.StatusOK},
		// Tenant Contributor can mutate any guest in the tenant.
		{"tenant-contrib mutates projY guest", cC, http.MethodPost, "/api/tenants/" + tenantA + "/guests/pve01/qemu/102/start", "", http.StatusAccepted},
		// …but never exceeds Contributor: an Owner-only route (create project) → 403.
		{"tenant-contrib cannot do owner action", cC, http.MethodPost, "/api/tenants/" + tenantA + "/projects", `{"name":"New"}`, http.StatusForbidden},
	}
	// snapshots GET needs a stub.
	mock.OnSnapshots = func(context.Context, proxmox.GuestRef) ([]types.Snapshot, error) { return []types.Snapshot{}, nil }

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := hh.req(t, tt.cookie, tt.method, tt.target, tt.body)
			if rec.Code != tt.want {
				t.Fatalf("%s = %d, want %d (body %s)", tt.name, rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

// --- /api/admin/* rejects non-admins (403) ---

func TestAdminRoutesRejectNonAdmin(t *testing.T) {
	hh := newHarness(t, &proxmoxtest.MockClient{})
	tenantA := hh.fake.AddTenant("A", "a")
	userA := hh.fake.AddUser("a@x.io", "Ada", false) // NOT platform-admin
	hh.fake.AddMembership(userA, "tenant", tenantA, "owner")
	c := hh.cookie(t, userA)

	for _, target := range []string{"/api/admin/tenants", "/api/admin/resources", "/api/admin/nodes", "/api/admin/pools"} {
		if rec := hh.req(t, c, http.MethodGet, target, ""); rec.Code != http.StatusForbidden {
			t.Errorf("non-admin GET %s = %d, want 403", target, rec.Code)
		}
	}

	// A platform-admin passes RequirePlatformAdmin.
	admin := hh.fake.AddUser("root@x.io", "Root", true)
	ca := hh.cookie(t, admin)
	if rec := hh.req(t, ca, http.MethodGet, "/api/admin/tenants", ""); rec.Code != http.StatusOK {
		t.Errorf("admin GET /api/admin/tenants = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
}

// --- project delete only when empty (409 otherwise) ---

func TestProjectDeleteOnlyWhenEmpty(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnDeletePool: func(context.Context, string) error { return nil },
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projFull := hh.fake.AddProject(tenantA, "Full", "full", "pc-a-full")
	projEmpty := hh.fake.AddProject(tenantA, "Empty", "empty", "pc-a-empty")
	owner := hh.fake.AddUser("o@x.io", "Ojo", false)
	hh.fake.AddMembership(owner, "tenant", tenantA, "owner")
	hh.fake.AddOwnership(tenantA, projFull, 101, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, owner)

	// Non-empty project → 409 conflict, pool never touched.
	if rec := hh.req(t, c, http.MethodDelete, "/api/tenants/"+tenantA+"/projects/"+projFull, `{"confirmName":"Full"}`); rec.Code != http.StatusConflict {
		t.Fatalf("delete non-empty project = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	// Wrong confirm name → 400.
	if rec := hh.req(t, c, http.MethodDelete, "/api/tenants/"+tenantA+"/projects/"+projEmpty, `{"confirmName":"WRONG"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("delete with wrong confirm = %d, want 400", rec.Code)
	}
	// Empty project, correct confirm → 204.
	if rec := hh.req(t, c, http.MethodDelete, "/api/tenants/"+tenantA+"/projects/"+projEmpty, `{"confirmName":"Empty"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("delete empty project = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
}

// --- scoped resources: only the tenant's owned guests, project/creator enriched ---

func TestScopedResourcesFiltersByTenant(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) {
			return []proxmox.RawResource{
				{ID: "qemu/101", Type: "qemu", VMID: 101, Name: "web-a", Node: "pve01", Status: "running"},
				{ID: "qemu/202", Type: "qemu", VMID: 202, Name: "web-b", Node: "pve01", Status: "running"},
				{ID: "node/pve01", Type: "node", Node: "pve01"},
			}, nil
		},
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	tenantB := hh.fake.AddTenant("B", "b")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	projB := hh.fake.AddProject(tenantB, "Web", "web", "pc-b-web")
	creator := hh.fake.AddUser("maker@x.io", "Maker", false)
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "reader")
	hh.fake.AddOwnership(tenantA, projA, 101, "qemu", "pve01", "active", strptr(creator))
	hh.fake.AddOwnership(tenantB, projB, 202, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userA)

	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/resources", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("scoped resources = %d (body %s)", rec.Code, rec.Body.String())
	}
	got := decodeBody[[]types.GuestSummary](t, rec)
	if len(got) != 1 || got[0].VMID != 101 {
		t.Fatalf("scoped resources = %+v, want only VMID 101 (tenant B's 202 must be filtered)", got)
	}
	if got[0].ProjectName != "Web" || got[0].CreatedBy != "Maker" {
		t.Fatalf("enrichment = project %q creator %q, want Web/Maker", got[0].ProjectName, got[0].CreatedBy)
	}

	// A projectId filter that matches nothing yields an empty list, not a leak.
	rec = hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/resources?projectId="+projB, "")
	if got := decodeBody[[]types.GuestSummary](t, rec); len(got) != 0 {
		t.Fatalf("cross-tenant projectId filter returned %+v, want empty", got)
	}
}

// --- create: pending → finalize on success ---

func TestCreateFinalizesOwnershipOnSuccess(t *testing.T) {
	createUPID := proxmox.UPID("UPID:pve01:1:2:3:vzcreate:150:u@pam:")
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return nil, nil },
		OnCreatePool:       func(context.Context, string, string) error { return nil },
		OnCreateLXC:        func(context.Context, string, map[string]any) (proxmox.UPID, error) { return createUPID, nil },
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	c := hh.cookie(t, userA)

	body := `{"type":"lxc","name":"cache-01","node":"pve01","vmid":150,"projectId":"` + projA + `",
		"source":{"mode":"vztmpl","vztmplVolId":"local:vztmpl/x.tar.gz"},
		"cores":1,"memoryMb":512,"diskGb":8,"storage":"local","bridge":"vmbr0"}`
	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/guests", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	// A pending reservation exists immediately.
	if s := hh.fake.OwnershipStatus(150); s != "pending" {
		t.Fatalf("reservation status = %q, want pending", s)
	}
	// Wait until the engine goroutine has submitted+tracked the create task, then
	// (as the single watcher would) complete it → engine finalizes the reservation.
	waitTracked(t, hh.registry, createUPID)
	hh.registry.Complete(createUPID, true, "OK")
	waitOwnership(t, hh.fake, 150, "active")
}

// --- delete releases ownership so a deleted VMID is reusable (regression) ---
//
// Bug: DELETE left resource_ownership at status=active, so the vmid UNIQUE
// constraint made every future reservation of that VMID a phantom 409 "already
// reserved or in use". This drives the full create→delete→create cycle on ONE
// VMID and asserts the second create succeeds.
func TestCreateDeleteCreateSameVMIDSucceeds(t *testing.T) {
	const vmid = 250
	createUPID1 := proxmox.UPID("UPID:pve01:1:2:3:vzcreate:250:u@pam:c1")
	createUPID2 := proxmox.UPID("UPID:pve01:1:2:3:vzcreate:250:u@pam:c2")
	deleteUPID := proxmox.UPID("UPID:pve01:1:2:3:vzdestroy:250:u@pam:")
	var createCalls int32
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return nil, nil },
		OnCreatePool:       func(context.Context, string, string) error { return nil },
		OnCreateLXC: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			if atomic.AddInt32(&createCalls, 1) == 1 {
				return createUPID1, nil
			}
			return createUPID2, nil
		},
		OnGuestStatus: func(context.Context, proxmox.GuestRef) (*proxmox.GuestStatusInfo, error) {
			return &proxmox.GuestStatusInfo{Status: "stopped", Name: "cache-01"}, nil
		},
		OnDeleteGuest: func(context.Context, proxmox.GuestRef, bool) (proxmox.UPID, error) {
			return deleteUPID, nil
		},
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	c := hh.cookie(t, userA)

	body := `{"type":"lxc","name":"cache-01","node":"pve01","vmid":250,"projectId":"` + projA + `",
		"source":{"mode":"vztmpl","vztmplVolId":"local:vztmpl/x.tar.gz"},
		"cores":1,"memoryMb":512,"diskGb":8,"storage":"local","bridge":"vmbr0"}`

	// 1. Create the guest and finalize its reservation to active.
	if rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/guests", body); rec.Code != http.StatusAccepted {
		t.Fatalf("create #1 = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	waitTracked(t, hh.registry, createUPID1)
	hh.registry.Complete(createUPID1, true, "OK")
	waitOwnership(t, hh.fake, vmid, "active")

	// 2. Delete it; on destroy completion the ownership row is tombstoned.
	if rec := hh.req(t, c, http.MethodDelete, "/api/tenants/"+tenantA+"/guests/pve01/lxc/250", `{"confirmName":"cache-01"}`); rec.Code != http.StatusAccepted {
		t.Fatalf("delete = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	waitTracked(t, hh.registry, deleteUPID)
	hh.registry.Complete(deleteUPID, true, "OK")
	waitOwnership(t, hh.fake, vmid, "tombstoned")

	// 3. Re-create the SAME VMID: the tombstoned row must be revived, not 409.
	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/guests", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create #2 on the reused VMID = %d, want 202 — a deleted VMID must be reservable again (body %s)",
			rec.Code, rec.Body.String())
	}
	waitTracked(t, hh.registry, createUPID2)
	hh.registry.Complete(createUPID2, true, "OK")
	waitOwnership(t, hh.fake, vmid, "active")
}

// --- a delete task stays pollable after its VMID's ownership is tombstoned ---
//
// Regression: releasing ownership on delete tombstones the VMID, but the caller
// must still be able to poll the very delete task to completion (checkTaskOwnership
// uses the task-read resolver, which accepts a tombstoned row for the owning
// tenant). Otherwise the destroy task 404s the instant the tombstone lands.
func TestDeleteTaskPollableAfterTombstone(t *testing.T) {
	const vmid = 8010
	taskUPID := "UPID:pve01:0001:0002:0003:vzdestroy:8010:root@pam:"
	mock := &proxmoxtest.MockClient{
		OnTaskStatus: func(_ context.Context, upid proxmox.UPID) (*proxmox.TaskInfo, error) {
			return &proxmox.TaskInfo{UPID: upid, Node: "pve01", Type: "vzdestroy", ID: "8010", StartTime: 10, EndTime: 20, ExitStatus: "OK"}, nil
		},
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	// The guest was deleted: the delete-release tombstoned its ownership row.
	ownID := hh.fake.AddOwnership(tenantA, projA, vmid, "lxc", "pve01", "active", nil)
	if err := hh.fake.TombstoneOwnership(context.Background(), ownID); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	up := url.PathEscape(taskUPID)

	// The owning tenant polls the delete task to completion (was 404 before).
	if rec := hh.req(t, hh.cookie(t, userA), http.MethodGet, "/api/tenants/"+tenantA+"/tasks/"+up, ""); rec.Code != http.StatusOK {
		t.Fatalf("owner delete-task poll after tombstone = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	// A different tenant still gets 404 — the tombstoned row keeps its owner.
	tenantB := hh.fake.AddTenant("B", "b")
	userB := hh.fake.AddUser("b@x.io", "Bo", false)
	hh.fake.AddMembership(userB, "tenant", tenantB, "contributor")
	if rec := hh.req(t, hh.cookie(t, userB), http.MethodGet, "/api/tenants/"+tenantB+"/tasks/"+up, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete-task poll = %d, want 404", rec.Code)
	}
}

// waitTracked blocks until upid is a running tracked task (the engine goroutine
// has submitted it) so a subsequent Complete is never lost to a race.
func waitTracked(t *testing.T, reg *tasks.Registry, upid proxmox.UPID) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, u := range reg.Running() {
			if u == upid {
				return
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("create task %s was never tracked", upid)
}

// --- create: pending → release on failure ---

func TestCreateReleasesOwnershipOnFailure(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return nil, nil },
		OnCreatePool:       func(context.Context, string, string) error { return nil },
		OnCreateLXC: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			return "", &types.APIError{Code: "proxmox_error", Message: "no space left", Status: 502}
		},
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	c := hh.cookie(t, userA)

	body := `{"type":"lxc","name":"cache-02","node":"pve01","vmid":151,"projectId":"` + projA + `",
		"source":{"mode":"vztmpl","vztmplVolId":"local:vztmpl/x.tar.gz"},
		"cores":1,"memoryMb":512,"diskGb":8,"storage":"local","bridge":"vmbr0"}`
	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/guests", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	// The create fails asynchronously → the reservation is released (row gone).
	waitOwnership(t, hh.fake, 151, "")
}

// --- create: duplicate VMID reservation → 409 ---

func TestCreateDuplicateVMIDConflict(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return nil, nil },
		OnCreatePool:       func(context.Context, string, string) error { return nil },
	}
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	userA := hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userA, "tenant", tenantA, "contributor")
	hh.fake.AddOwnership(tenantA, projA, 160, "lxc", "pve01", "active", nil) // already taken
	c := hh.cookie(t, userA)

	body := `{"type":"lxc","name":"dup","node":"pve01","vmid":160,"projectId":"` + projA + `",
		"source":{"mode":"vztmpl","vztmplVolId":"local:vztmpl/x.tar.gz"},
		"cores":1,"memoryMb":512,"diskGb":8,"storage":"local","bridge":"vmbr0"}`
	if rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/guests", body); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate vmid create = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

func waitOwnership(t *testing.T, fake *storetest.Fake, vmid int, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if fake.OwnershipStatus(vmid) == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("ownership for vmid %d = %q, want %q", vmid, fake.OwnershipStatus(vmid), want)
}
