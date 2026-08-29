package handlers_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/catalog"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
	"github.com/timkrebs9/proxcloud/backend/internal/deploy"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

// newSetHarness wires the real router with the catalog AND deployment sets enabled:
// a fake snippet writer, a deterministic readiness probe (so a member reaches ready
// without a live listener), and a fast configuring poll.
func newSetHarness(t *testing.T, mock *proxmoxtest.MockClient) (*harness, *catalogFakeWriter) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	fake := storetest.New()
	sessions := auth.NewSessions(fake, false, false, time.Hour, 24*time.Hour)
	authH := &auth.Handler{Sessions: sessions, Store: fake, Hasher: auth.NewHasher(), Log: log}
	mw := &authz.Middleware{Store: fake, Log: log}
	reg := tasks.NewRegistry()
	broker := events.NewBroker()
	engine := deploy.NewEngine(mock, reg, broker, log)
	engine.Finalize = func(ctx context.Context, id, upid string) error { return fake.FinalizeOwnership(ctx, id, upid) }
	engine.Release = func(ctx context.Context, id string) error { return fake.ReleaseOwnership(ctx, id) }
	writer := &catalogFakeWriter{written: make(chan writtenSnippet, 8)}
	engine.Snippets = writer
	engine.ConfigurePoll = 5 * time.Millisecond
	engine.ConfigureTimeout = 3 * time.Second
	engine.ProbeTimeout = 20 * time.Millisecond
	engine.Probe = func(string, int, time.Duration) error { return nil }

	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	api := &handlers.Deps{PVE: mock, Log: log, Store: fake, Authz: mw, Deploy: engine, Registry: reg, Broker: broker,
		Catalog: cat, CatalogEnabled: true, SnippetDatastore: "local", DeploymentSetsEnabled: true}
	h := httpserver.New(httpserver.Deps{
		Cfg: &config.Config{}, Log: log, Auth: authH, Authz: mw,
		Account: api.MountAccount, Admin: api.MountAdmin, Tenant: api.MountTenant,
	})
	return &harness{h: h, fake: fake, mock: mock, engine: engine, registry: reg, sessions: sessions}, writer
}

// createSetBody is the default valid CreateSetRequest: a k3s-cluster with 1 server
// (static IP) + 2 agents, one SSH key.
func createSetBody(projID string) string {
	req := types.CreateSetRequest{
		ServiceId: "k3s-cluster", ProjectId: projID, Name: "cluster1", Node: "pve01",
		Storage: "local-lvm", Bridge: "vmbr0",
		SSHKeys:    []string{"ssh-ed25519 AAAAExampleKey user@host"},
		ServerVMID: 201, AgentVMIDs: []int{202, 203},
		ServerIP: &types.IPConfig{Mode: "static", CIDR: "192.168.1.50/24", Gateway: "192.168.1.1"},
	}
	b, _ := json.Marshal(req)
	return string(b)
}

// completeSetTasks drains created and completes each task once tracked (no t.Fatalf,
// so it is safe from a background goroutine).
func completeSetTasks(reg *tasks.Registry, created <-chan proxmox.UPID, done <-chan struct{}) {
	complete := func(u proxmox.UPID) {
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			for _, ru := range reg.Running() {
				if ru == u {
					reg.Complete(u, true, "OK")
					return
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	for {
		select {
		case u := <-created:
			complete(u)
		case <-done:
			return
		}
	}
}

// TestCreateSetHappyPathTokenPropagation drives a whole cluster to ready and proves
// the shared join token propagates to EVERY member as a base64 blob only — the raw
// token never appears in any snippet — plus the static server IP reaches server
// (tls-san) and agents (K3S_URL). Also proves server-first sequencing (the server
// snippet is written before the agents').
func TestCreateSetHappyPathTokenPropagation(t *testing.T) {
	created := make(chan proxmox.UPID, 16)
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return nil, nil },
		OnCreatePool:       func(context.Context, string, string) error { return nil },
		OnCreateVM: func(_ context.Context, _ string, params map[string]any) (proxmox.UPID, error) {
			u := proxmox.UPID("UPID:pve01:1:1:1:qmcreate:" + itoa(intOf(params["vmid"])) + ":u@pam:")
			created <- u
			return u, nil
		},
		OnGuestAction: func(_ context.Context, ref proxmox.GuestRef, _ string) (proxmox.UPID, error) {
			u := proxmox.UPID("UPID:pve01:2:2:2:qmstart:" + itoa(ref.VMID) + ":u@pam:")
			created <- u
			return u, nil
		},
		OnAgentInterfaces: func(context.Context, proxmox.GuestRef) ([]types.GuestNIC, error) {
			return []types.GuestNIC{{Name: "eth0", IPv4: []string{"10.0.0.9/24"}}}, nil
		},
	}
	hh, writer := newSetHarness(t, mock)
	tenantID, projID, userID := seedTenant(hh, "contributor")
	c := hh.cookie(t, userID)

	done := make(chan struct{})
	go completeSetTasks(hh.registry, created, done)
	defer close(done)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/deployment-sets", createSetBody(projID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("create set = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	resp := decodeBody[types.CreateSetResponse](t, rec)
	if resp.SetID == "" || resp.Status != "provisioning" || len(resp.Members) != 3 {
		t.Fatalf("response = %+v", resp)
	}

	// Wait for the durable status to reach ready (all members up).
	deadline := time.Now().Add(10 * time.Second)
	for hh.fake.SetStatus(resp.SetID) != "ready" {
		if time.Now().After(deadline) {
			t.Fatalf("set never became ready (status %q)", hh.fake.SetStatus(resp.SetID))
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Collect the three rendered snippets.
	snips := map[string]string{}
	order := []string{}
	for i := 0; i < 3; i++ {
		select {
		case w := <-writer.written:
			snips[w.name] = w.content
			order = append(order, w.name)
		case <-time.After(2 * time.Second):
			t.Fatalf("expected 3 snippets, got %d", len(snips))
		}
	}
	if order[0] != "proxcloud-201-k3s-cluster.yaml" {
		t.Fatalf("server snippet must be written FIRST; order=%v", order)
	}
	serverSnip := snips["proxcloud-201-k3s-cluster.yaml"]
	agent1 := snips["proxcloud-202-k3s-cluster.yaml"]
	agent2 := snips["proxcloud-203-k3s-cluster.yaml"]
	if serverSnip == "" || agent1 == "" || agent2 == "" {
		t.Fatalf("missing a member snippet: %v", order)
	}

	// The SAME base64 join token must appear in all three snippets.
	tokenRe := regexp.MustCompile(`K3S_TOKEN="\$\(printf %s '([A-Za-z0-9+/=]+)'`)
	m := tokenRe.FindStringSubmatch(serverSnip)
	if m == nil {
		t.Fatalf("no base64 K3S_TOKEN in the server snippet:\n%s", serverSnip)
	}
	tokenB64 := m[1]
	for name, content := range snips {
		if !strings.Contains(content, tokenB64) {
			t.Errorf("member %s snippet is missing the shared base64 token", name)
		}
	}

	// The RAW token must never appear literally — only its base64 form (ADR-0030).
	raw, err := base64.StdEncoding.DecodeString(tokenB64)
	if err != nil || len(raw) == 0 {
		t.Fatalf("token blob is not valid base64: %v", err)
	}
	for name, content := range snips {
		if strings.Contains(content, string(raw)) {
			t.Errorf("RAW join token leaked into the %s snippet (must be base64-only)", name)
		}
	}

	// The static server IP reaches the server (tls-san via SERVER_IP) and the agents
	// (K3S_URL). Agents dial the API port at the static IP.
	if !strings.Contains(serverSnip, "192.168.1.50") {
		t.Error("server snippet is missing the static server IP")
	}
	for _, a := range []string{agent1, agent2} {
		if !strings.Contains(a, "https://192.168.1.50:6443") {
			t.Errorf("agent snippet is missing K3S_URL to the static server IP:\n%s", a)
		}
	}

	// Every member ownership was finalized (active), tagged to the set.
	for _, vmid := range []int{201, 202, 203} {
		if s := hh.fake.OwnershipStatus(vmid); s != "active" {
			t.Errorf("member %d ownership = %q, want active", vmid, s)
		}
	}

	// Audit records the set + service + member count, NEVER a token.
	var detail string
	for _, a := range hh.fake.AllAudit() {
		if a.Action == "deployment_set.create" {
			detail = string(a.Detail)
		}
	}
	if detail == "" {
		t.Fatal("no deployment_set.create audit row")
	}
	if !strings.Contains(detail, "member_count") || strings.Contains(detail, tokenB64) || strings.Contains(detail, string(raw)) {
		t.Fatalf("audit detail wrong (needs member_count, never the token): %s", detail)
	}
}

// TestCreateSetOverQuota409ZeroRows: the batch is checked before any Proxmox call;
// over quota returns 409 and leaves ZERO pending member rows and zero set rows.
func TestCreateSetOverQuota409ZeroRows(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		// Active guest 101 consumes the tenant's whole vCPU cap.
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) {
			return []proxmox.RawResource{{ID: "qemu/101", Type: "qemu", VMID: 101, Node: "pve01", MaxCPU: 2}}, nil
		},
		OnCreatePool: func(context.Context, string, string) error {
			t.Error("EnsureProjectPool must not run when over quota")
			return nil
		},
		OnCreateVM: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			t.Error("CreateVM must not run when over quota")
			return "", nil
		},
	}
	hh, _ := newSetHarness(t, mock)
	tenantID, projID, userID := seedTenant(hh, "contributor")
	hh.fake.AddQuota("tenant", tenantID, iptr(2), nil, nil, nil) // MaxVCPU=2
	hh.fake.AddOwnership(tenantID, projID, 101, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userID)

	// The cluster needs 3×2 = 6 vCPU on top of the 2 already used → refused.
	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/deployment-sets", createSetBody(projID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("over-quota set = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "quota_exceeded" {
		t.Fatalf("error code = %q, want quota_exceeded", env.Error.Code)
	}
	for _, vmid := range []int{201, 202, 203} {
		if s := hh.fake.OwnershipStatus(vmid); s != "" {
			t.Errorf("member %d reservation leaked: %q (want none)", vmid, s)
		}
	}
	sets, _ := hh.fake.ListDeploymentSets(context.Background(), tenantID)
	if len(sets) != 0 {
		t.Fatalf("an orphan set row survived an over-quota create: %+v", sets)
	}
}

// TestCreateSetCrossTenantProject404: a project owned by another tenant is a 404
// (no existence leak) before any reservation.
func TestCreateSetCrossTenantProject404(t *testing.T) {
	hh, _ := newSetHarness(t, &proxmoxtest.MockClient{})
	tenantID, _, userID := seedTenant(hh, "contributor")
	otherTenant := hh.fake.AddTenant("B", "b")
	otherProj := hh.fake.AddProject(otherTenant, "Other", "other", "pc-b-other")
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/deployment-sets", createSetBody(otherProj))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant project set = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestCreateSetReaderBlocked: a Reader cannot create a set (Contributor mutation).
func TestCreateSetReaderBlocked(t *testing.T) {
	hh, _ := newSetHarness(t, &proxmoxtest.MockClient{})
	tenantID, projID, userID := seedTenant(hh, "reader")
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/deployment-sets", createSetBody(projID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reader create set = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestSetActionReaderBlocked: a Reader cannot start/stop a set.
func TestSetActionReaderBlocked(t *testing.T) {
	hh, _ := newSetHarness(t, &proxmoxtest.MockClient{})
	tenantID, projID, userID := seedTenant(hh, "reader")
	setID := seedSet(t, hh, tenantID, projID)
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/deployment-sets/"+setID+"/start", "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reader set action = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestSetActionStartFansOut: start fans out over the members, server first (control
// plane before workers, ADR-0030), returning one task per member.
func TestSetActionStartFansOut(t *testing.T) {
	var order []int
	mock := &proxmoxtest.MockClient{
		OnGuestAction: func(_ context.Context, ref proxmox.GuestRef, action string) (proxmox.UPID, error) {
			if action != "start" {
				t.Errorf("action = %q, want start", action)
			}
			order = append(order, ref.VMID)
			return proxmox.UPID("UPID:pve01:1:1:1:qmstart:" + itoa(ref.VMID) + ":u@pam:"), nil
		},
	}
	hh, _ := newSetHarness(t, mock)
	tenantID, projID, userID := seedTenant(hh, "contributor")
	setID := seedSet(t, hh, tenantID, projID)
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/deployment-sets/"+setID+"/start", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("set start = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	resp := decodeBody[types.SetActionResponse](t, rec)
	if len(resp.Tasks) != 3 {
		t.Fatalf("start tasks = %d, want 3", len(resp.Tasks))
	}
	if len(order) == 0 || order[0] != 201 {
		t.Fatalf("start order = %v, want the server (201) first", order)
	}
}

// TestGetSetCrossTenant404: a set owned by another tenant is an indistinguishable
// 404 for both GET and DELETE (tenancy iron rule: 404 never 403).
func TestGetSetCrossTenant404(t *testing.T) {
	hh, _ := newSetHarness(t, &proxmoxtest.MockClient{})
	tenantID, _, userID := seedTenant(hh, "contributor")
	otherTenant := hh.fake.AddTenant("B", "b")
	otherProj := hh.fake.AddProject(otherTenant, "Other", "other", "pc-b-other")
	otherSet := seedSet(t, hh, otherTenant, otherProj)
	c := hh.cookie(t, userID)

	if rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantID+"/deployment-sets/"+otherSet, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant GET set = %d, want 404", rec.Code)
	}
	if rec := hh.req(t, c, http.MethodDelete, "/api/tenants/"+tenantID+"/deployment-sets/"+otherSet, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant DELETE set = %d, want 404", rec.Code)
	}
}

// TestListAndGetSet: the sets gallery + detail reconstruct members from ownership
// rows (durable truth), Reader-authorized.
func TestListAndGetSet(t *testing.T) {
	hh, _ := newSetHarness(t, &proxmoxtest.MockClient{})
	tenantID, projID, userID := seedTenant(hh, "reader")
	setID := seedSet(t, hh, tenantID, projID)
	c := hh.cookie(t, userID)

	list := decodeBody[types.DeploymentSetList](t, hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantID+"/deployment-sets", ""))
	if len(list.Sets) != 1 || list.Sets[0].ID != setID {
		t.Fatalf("list sets = %+v, want the one seeded set", list.Sets)
	}
	got := decodeBody[types.DeploymentSet](t, hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantID+"/deployment-sets/"+setID, ""))
	if got.ID != setID || len(got.Members) != 3 {
		t.Fatalf("get set = %+v, want 3 members", got)
	}
	// Server sorts before agents (role DESC in the store).
	if got.Members[0].Role != "server" {
		t.Errorf("first member role = %q, want server", got.Members[0].Role)
	}
}

// TestDeleteSetTombstonesMembers: delete tears every member down (purge), tombstones
// ALL ownership rows (freeing the VMIDs), and removes the set row.
func TestDeleteSetTombstonesMembers(t *testing.T) {
	deleted := make(chan proxmox.UPID, 8)
	mock := &proxmoxtest.MockClient{
		OnDeleteGuest: func(_ context.Context, ref proxmox.GuestRef, purge bool) (proxmox.UPID, error) {
			if !purge {
				t.Error("set delete must purge")
			}
			u := proxmox.UPID("UPID:pve01:9:9:9:qmdestroy:" + itoa(ref.VMID) + ":u@pam:")
			deleted <- u
			return u, nil
		},
	}
	hh, _ := newSetHarness(t, mock)
	tenantID, projID, userID := seedTenant(hh, "contributor")
	setID := seedSet(t, hh, tenantID, projID)
	c := hh.cookie(t, userID)

	done := make(chan struct{})
	go completeSetTasks(hh.registry, deleted, done)
	defer close(done)

	rec := hh.req(t, c, http.MethodDelete, "/api/tenants/"+tenantID+"/deployment-sets/"+setID, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("delete set = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	resp := decodeBody[types.SetActionResponse](t, rec)
	if len(resp.Tasks) != 3 {
		t.Fatalf("delete tasks = %d, want 3 (one per member)", len(resp.Tasks))
	}

	// The tombstone-after-destroy goroutines run once each destroy task completes.
	deadline := time.Now().Add(5 * time.Second)
	for {
		allTomb := hh.fake.OwnershipStatus(201) == "tombstoned" &&
			hh.fake.OwnershipStatus(202) == "tombstoned" &&
			hh.fake.OwnershipStatus(203) == "tombstoned"
		if allTomb {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("members not all tombstoned: 201=%q 202=%q 203=%q",
				hh.fake.OwnershipStatus(201), hh.fake.OwnershipStatus(202), hh.fake.OwnershipStatus(203))
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The set row is gone.
	if _, err := hh.fake.GetDeploymentSet(context.Background(), tenantID, setID); err == nil {
		t.Fatal("set row still exists after delete")
	}
}

// TestDeleteSetPartialFailureKeepsSet: when ONE member's destroy fails to submit, the
// set row is KEPT (honest 'deleting' status, a 502 — not a 202 "all good"), the
// members that DID submit are torn down + tombstoned, and the failed member's guest
// is left intact (its ownership NOT tombstoned) so the delete stays retryable. This
// prevents a transient PVE failure from deleting the set row while an orphan guest
// keeps running and quota-charged.
func TestDeleteSetPartialFailureKeepsSet(t *testing.T) {
	deleted := make(chan proxmox.UPID, 8)
	mock := &proxmoxtest.MockClient{
		OnDeleteGuest: func(_ context.Context, ref proxmox.GuestRef, purge bool) (proxmox.UPID, error) {
			// Agent 202's destroy fails to submit; the server + other agent succeed.
			if ref.VMID == 202 {
				return "", errors.New("storage is busy")
			}
			u := proxmox.UPID("UPID:pve01:9:9:9:qmdestroy:" + itoa(ref.VMID) + ":u@pam:")
			deleted <- u
			return u, nil
		},
	}
	hh, _ := newSetHarness(t, mock)
	tenantID, projID, userID := seedTenant(hh, "contributor")
	setID := seedSet(t, hh, tenantID, projID)
	c := hh.cookie(t, userID)

	done := make(chan struct{})
	go completeSetTasks(hh.registry, deleted, done)
	defer close(done)

	rec := hh.req(t, c, http.MethodDelete, "/api/tenants/"+tenantID+"/deployment-sets/"+setID, "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("partial delete = %d, want 502 (body %s)", rec.Code, rec.Body.String())
	}
	if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "set_teardown_incomplete" {
		t.Fatalf("error code = %q, want set_teardown_incomplete", env.Error.Code)
	}

	// The two submittable members tombstone once their destroy tasks complete.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if hh.fake.OwnershipStatus(201) == "tombstoned" && hh.fake.OwnershipStatus(203) == "tombstoned" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("submittable members not tombstoned: 201=%q 203=%q",
				hh.fake.OwnershipStatus(201), hh.fake.OwnershipStatus(203))
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The failed member's guest is left running — its ownership must NOT be tombstoned.
	if s := hh.fake.OwnershipStatus(202); s != "active" {
		t.Errorf("failed member 202 ownership = %q, want active (guest still running, quota-charged)", s)
	}

	// The set row survives with an honest 'deleting' status so the UI keeps it and the
	// delete can be retried — the fix's core: the row is NOT deleted on partial failure.
	got, err := hh.fake.GetDeploymentSet(context.Background(), tenantID, setID)
	if err != nil {
		t.Fatalf("set row must survive a partial teardown (got err %v)", err)
	}
	if got.Status != "deleting" {
		t.Errorf("set status = %q, want deleting (honest retry state)", got.Status)
	}
}

// TestDeploymentSetsDisabled404: with the feature off (default harness), the routes
// are still mounted (completeness) but the handlers report not-enabled as 404.
func TestDeploymentSetsDisabled404(t *testing.T) {
	hh := newHarness(t, &proxmoxtest.MockClient{}) // default harness: sets off
	tenantID, projID, userID := seedTenant(hh, "contributor")
	c := hh.cookie(t, userID)

	if rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantID+"/deployment-sets", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("list (disabled) = %d, want 404", rec.Code)
	}
	if rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/deployment-sets", createSetBody(projID)); rec.Code != http.StatusNotFound {
		t.Fatalf("create (disabled) = %d, want 404", rec.Code)
	}
}

// seedSet stores a ready set with a server + 2 agents (active, tagged to the set) so
// the read/action/delete handlers have realistic member rows to operate on.
func seedSet(t *testing.T, hh *harness, tenantID, projID string) string {
	t.Helper()
	setID := hh.fake.AddDeploymentSet(tenantID, projID, "k3s-cluster", "ready")
	members := []store.BatchMember{
		{VMID: 201, GuestType: "qemu", Node: "pve01", Role: "server", Reserved: store.Alloc{VCPU: 2, RAMMB: 2048, DiskGB: 20}},
		{VMID: 202, GuestType: "qemu", Node: "pve01", Role: "agent", Reserved: store.Alloc{VCPU: 2, RAMMB: 2048, DiskGB: 20}},
		{VMID: 203, GuestType: "qemu", Node: "pve01", Role: "agent", Reserved: store.Alloc{VCPU: 2, RAMMB: 2048, DiskGB: 20}},
	}
	reserved, err := hh.fake.ReserveOwnershipBatch(context.Background(), store.ReserveOwnershipBatchParams{
		TenantID: tenantID, ProjectID: projID, SetID: setID, Members: members,
	})
	if err != nil {
		t.Fatalf("seed set batch: %v", err)
	}
	for _, o := range reserved {
		if err := hh.fake.FinalizeOwnership(context.Background(), o.ID, "UPID:seed"); err != nil {
			t.Fatalf("finalize seeded member: %v", err)
		}
	}
	return setID
}

// intOf coerces a map value (int stored in the create params) to int.
func intOf(v any) int {
	if i, ok := v.(int); ok {
		return i
	}
	return 0
}

// itoa is a tiny int->string for building deterministic UPIDs in the mock.
func itoa(n int) string {
	return strconv.Itoa(n)
}
