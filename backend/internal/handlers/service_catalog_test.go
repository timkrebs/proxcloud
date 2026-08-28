package handlers_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
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
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

// catalogFakeWriter is a deploy.SnippetWriter double for the handler tests: it
// records what the deploy engine wrote so a test can assert no raw credential
// reached the snippet.
type catalogFakeWriter struct {
	written chan writtenSnippet
}

type writtenSnippet struct {
	name, content string
}

func (w *catalogFakeWriter) WriteSnippet(_ context.Context, name, content string) error {
	select {
	case w.written <- writtenSnippet{name, content}:
	default:
	}
	return nil
}
func (w *catalogFakeWriter) RemoveSnippet(context.Context, string) error { return nil }

// newCatalogHarness wires the real router with CATALOG_ENABLED on: the loaded
// embedded catalog, a fake snippet writer on the engine, and a fast configuring
// poll so an async deployment does not linger.
func newCatalogHarness(t *testing.T, mock *proxmoxtest.MockClient) (*harness, *catalogFakeWriter) {
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
	writer := &catalogFakeWriter{written: make(chan writtenSnippet, 4)}
	engine.Snippets = writer
	engine.ConfigurePoll = 5 * time.Millisecond
	engine.ConfigureTimeout = 500 * time.Millisecond
	engine.ProbeTimeout = 20 * time.Millisecond

	cat, err := catalog.Load()
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	api := &handlers.Deps{PVE: mock, Log: log, Store: fake, Authz: mw, Deploy: engine, Registry: reg, Broker: broker,
		Catalog: cat, CatalogEnabled: true, SnippetDatastore: "local"}
	h := httpserver.New(httpserver.Deps{
		Cfg: &config.Config{}, Log: log, Auth: authH, Authz: mw,
		Account: api.MountAccount, Admin: api.MountAdmin, Tenant: api.MountTenant,
	})
	return &harness{h: h, fake: fake, mock: mock, engine: engine, registry: reg, sessions: sessions}, writer
}

// seedTenant creates a tenant + project + user with the given tenant role.
func seedTenant(hh *harness, role string) (tenantID, projID, userID string) {
	tenantID = hh.fake.AddTenant("A", "a")
	projID = hh.fake.AddProject(tenantID, "Web", "web", "pc-a-web")
	userID = hh.fake.AddUser("a@x.io", "Ada", false)
	hh.fake.AddMembership(userID, "tenant", tenantID, role)
	return
}

func provisionBody(projID string) string {
	// A catalog guest locks password login, so an SSH key is mandatory (a guest
	// with none is unreachable). The default body supplies one.
	return `{"projectId":"` + projID + `","name":"pg-01","node":"pve01","vmid":106,` +
		`"storage":"local-lvm","bridge":"vmbr0",` +
		`"sshKeys":["ssh-ed25519 AAAAExampleKey user@host"]}`
}

// provisionBodyNoKey mirrors provisionBody but omits sshKeys, to prove an empty
// key set is rejected before any reservation.
func provisionBodyNoKey(projID string) string {
	return `{"projectId":"` + projID + `","name":"pg-01","node":"pve01","vmid":106,` +
		`"storage":"local-lvm","bridge":"vmbr0"}`
}

// provisionBodyWithCred builds a Phase-C provision body carrying a user-supplied
// superuser credential. It marshals via encoding/json so hostile metacharacters in
// the password survive intact. An empty username is dropped (omitempty).
func provisionBodyWithCred(projID, username, password string) string {
	req := types.ProvisionServiceRequest{
		ProjectId: projID, Name: "pg-01", Node: "pve01", VMID: 106,
		Storage: "local-lvm", Bridge: "vmbr0",
		SSHKeys:     []string{"ssh-ed25519 AAAAExampleKey user@host"},
		Credentials: []types.ProvisionCredential{{Name: "superuser", Username: username, Password: password}},
	}
	b, _ := json.Marshal(req)
	return string(b)
}

func TestListServices(t *testing.T) {
	hh, _ := newCatalogHarness(t, &proxmoxtest.MockClient{})
	tenantID, _, userID := seedTenant(hh, "reader")
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantID+"/service-catalog", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	list := decodeBody[types.CatalogServiceList](t, rec)
	found := false
	for _, s := range list.Services {
		if s.ID == "postgresql" {
			found = true
		}
	}
	if !found {
		t.Fatalf("postgresql not in catalog list: %+v", list.Services)
	}
}

func TestGetServiceUnknown404(t *testing.T) {
	hh, _ := newCatalogHarness(t, &proxmoxtest.MockClient{})
	tenantID, _, userID := seedTenant(hh, "reader")
	c := hh.cookie(t, userID)

	if rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantID+"/service-catalog/postgresql", ""); rec.Code != http.StatusOK {
		t.Fatalf("get known = %d, want 200", rec.Code)
	}
	if rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantID+"/service-catalog/nope", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get unknown = %d, want 404", rec.Code)
	}
}

func TestProvisionServiceReserveAndSubmit(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return nil, nil },
		OnCreatePool:       func(context.Context, string, string) error { return nil },
		OnCreateVM: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			return "UPID:pve01:1:1:1:qmcreate:106:u@pam:", nil
		},
	}
	hh, writer := newCatalogHarness(t, mock)
	tenantID, projID, userID := seedTenant(hh, "contributor")
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/service-catalog/postgresql/provision", provisionBody(projID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("provision = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	resp := decodeBody[types.ProvisionServiceResponse](t, rec)
	if resp.DeploymentID == "" || resp.VMID != 106 {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Username != "postgres" || resp.GeneratedPassword == "" {
		t.Fatalf("generated credential not surfaced once: %+v", resp)
	}
	// The VMID was reserved (pending) before any Proxmox create.
	if s := hh.fake.OwnershipStatus(106); s != "pending" && s != "active" {
		t.Fatalf("ownership status = %q, want pending/active", s)
	}

	// The engine wrote the snippet; the RAW generated password must NOT be in it,
	// only its base64 blob (the in-guest decode is the only place it appears raw).
	select {
	case w := <-writer.written:
		if w.name != "proxcloud-106-postgresql.yaml" {
			t.Errorf("snippet name = %q", w.name)
		}
		if !strings.HasPrefix(w.content, "#cloud-config") {
			t.Errorf("snippet is not a cloud-config:\n%s", w.content)
		}
		if strings.Contains(w.content, resp.GeneratedPassword) {
			t.Error("RAW generated password leaked into the rendered snippet")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("engine never wrote the snippet")
	}

	// The mutation is audited as service_catalog.provision, records the
	// user_credentials boolean, and NEVER the password.
	var got *string
	for _, a := range hh.fake.AllAudit() {
		if a.Action == "service_catalog.provision" {
			s := string(a.Detail)
			got = &s
		}
	}
	if got == nil {
		t.Fatal("no service_catalog.provision audit row")
	}
	if !strings.Contains(*got, "user_credentials") || strings.Contains(*got, resp.GeneratedPassword) {
		t.Fatalf("audit detail wrong (must have user_credentials, never the password): %s", *got)
	}
}

// TestProvisionServiceUserSuppliedCredential is the Phase-C happy path: the user
// supplies their own superuser password (containing hostile metacharacters, ≥ 12
// chars). It is accepted, injected through the SAME base64 transport (raw value
// NEVER in the snippet), the response does NOT echo the value back but flags "you
// set this credential", and the audit boolean flips to user_credentials="true".
// TestProvisionServiceVaultNoCredential exercises the empty-credential-schema path
// (Vault, ADR-0027 §4): provisioning must NOT panic on resolved[0], must surface no
// generated password, and must carry a non-secret self-init hint.
func TestProvisionServiceVaultNoCredential(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return nil, nil },
		OnCreatePool:       func(context.Context, string, string) error { return nil },
		OnCreateVM: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			return "UPID:pve01:1:1:1:qmcreate:106:u@pam:", nil
		},
	}
	hh, writer := newCatalogHarness(t, mock)
	tenantID, projID, userID := seedTenant(hh, "contributor")
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/service-catalog/vault/provision", provisionBody(projID))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("vault provision = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	resp := decodeBody[types.ProvisionServiceResponse](t, rec)
	if resp.GeneratedPassword != "" {
		t.Fatalf("vault must surface NO generated password, got %+v", resp)
	}
	if resp.CredentialHint == "" {
		t.Fatalf("vault should carry a non-secret self-init credential hint")
	}
	select {
	case w := <-writer.written:
		if w.name != "proxcloud-106-vault.yaml" || !strings.HasPrefix(w.content, "#cloud-config") {
			t.Errorf("unexpected vault snippet: name=%q", w.name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("engine never wrote the vault snippet")
	}
}

func TestProvisionServiceUserSuppliedCredential(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) { return nil, nil },
		OnCreatePool:       func(context.Context, string, string) error { return nil },
		OnCreateVM: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			return "UPID:pve01:1:1:1:qmcreate:106:u@pam:", nil
		},
	}
	hh, writer := newCatalogHarness(t, mock)
	tenantID, projID, userID := seedTenant(hh, "contributor")
	c := hh.cookie(t, userID)

	// A deliberately hostile, ≥12-char password: quotes, $(...), backticks, pipe,
	// semicolon, YAML metacharacters. Length-only policy admits all of them.
	hostilePass := "P@ss $(reboot) `id` \"q\" | ; # :"

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/service-catalog/postgresql/provision", provisionBodyWithCred(projID, "", hostilePass))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("provision = %d, want 202 (body %s)", rec.Code, rec.Body.String())
	}
	resp := decodeBody[types.ProvisionServiceResponse](t, rec)
	if resp.Username != "postgres" {
		t.Fatalf("username = %q, want postgres", resp.Username)
	}
	// The user already has the value → it is NEVER echoed back.
	if resp.GeneratedPassword != "" {
		t.Fatalf("user-supplied credential must not be echoed back, got %q", resp.GeneratedPassword)
	}
	if resp.CredentialHint != "you set this credential" {
		t.Fatalf("hint = %q, want 'you set this credential'", resp.CredentialHint)
	}

	// The engine wrote the snippet: the RAW password must NOT appear, only its
	// base64 blob (the in-guest decode is the only place it is raw).
	select {
	case w := <-writer.written:
		for _, frag := range []string{hostilePass, "$(reboot)", "`id`"} {
			if strings.Contains(w.content, frag) {
				t.Errorf("raw user-supplied fragment %q leaked into the snippet", frag)
			}
		}
		if !strings.Contains(w.content, catalog.B64(hostilePass)) {
			t.Error("base64 of the user-supplied password missing from the snippet")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("engine never wrote the snippet")
	}

	// Audit records user_credentials="true" and NEVER the value.
	var got *string
	for _, a := range hh.fake.AllAudit() {
		if a.Action == "service_catalog.provision" {
			s := string(a.Detail)
			got = &s
		}
	}
	if got == nil {
		t.Fatal("no service_catalog.provision audit row")
	}
	if !strings.Contains(*got, `"user_credentials":"true"`) {
		t.Fatalf("audit must record user_credentials=true: %s", *got)
	}
	if strings.Contains(*got, hostilePass) || strings.Contains(*got, "$(reboot)") {
		t.Fatalf("audit detail leaked the user-supplied password: %s", *got)
	}
}

// TestProvisionServiceWeakPassword400: a sub-12-char user-supplied password is
// rejected 400 BEFORE any reservation (validate-before-reserve), and no VMID leaks.
func TestProvisionServiceWeakPassword400(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) {
			t.Error("clusterSnapshot must not run: credential validation precedes reservation")
			return nil, nil
		},
		OnCreateVM: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			t.Error("CreateVM must not run for a rejected credential")
			return "", nil
		},
	}
	hh, _ := newCatalogHarness(t, mock)
	tenantID, projID, userID := seedTenant(hh, "contributor")
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/service-catalog/postgresql/provision", provisionBodyWithCred(projID, "", "short11char")) // 11 chars
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("weak password = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "at least 12") {
		t.Errorf("error should state the length policy: %s", rec.Body.String())
	}
	if s := hh.fake.OwnershipStatus(106); s != "" {
		t.Fatalf("a reservation leaked for a rejected credential: %q", s)
	}
}

// TestProvisionServiceSuppliedUsernameOnFixed400: postgres fixes the username
// (`postgres`), so a supplied username is rejected 400 before any reservation.
func TestProvisionServiceSuppliedUsernameOnFixed400(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnCreateVM: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			t.Error("CreateVM must not run for a rejected credential")
			return "", nil
		},
	}
	hh, _ := newCatalogHarness(t, mock)
	tenantID, projID, userID := seedTenant(hh, "contributor")
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/service-catalog/postgresql/provision", provisionBodyWithCred(projID, "evil_admin", "correcthorse12"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("supplied username on fixed credential = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "fixed") {
		t.Errorf("error should explain the username is fixed: %s", rec.Body.String())
	}
	if s := hh.fake.OwnershipStatus(106); s != "" {
		t.Fatalf("a reservation leaked for a rejected credential: %q", s)
	}
}

// TestProvisionServiceRequiresSSHKey: a catalog guest locks password login, so a
// provision with no SSH key is rejected 400 — and no VMID reservation leaks.
func TestProvisionServiceRequiresSSHKey(t *testing.T) {
	mock := &proxmoxtest.MockClient{
		OnClusterResources: func(context.Context) ([]proxmox.RawResource, error) {
			t.Error("clusterSnapshot must not run: the SSH-key check precedes reservation")
			return nil, nil
		},
		OnCreatePool: func(context.Context, string, string) error {
			t.Error("EnsureProjectPool must not run without an SSH key")
			return nil
		},
		OnCreateVM: func(context.Context, string, map[string]any) (proxmox.UPID, error) {
			t.Error("CreateVM must not run without an SSH key")
			return "", nil
		},
	}
	hh, _ := newCatalogHarness(t, mock)
	tenantID, projID, userID := seedTenant(hh, "contributor")
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/service-catalog/postgresql/provision", provisionBodyNoKey(projID))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("provision without ssh key = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "SSH public key") {
		t.Errorf("error message should explain the SSH-key requirement: %s", rec.Body.String())
	}
	// No reservation may leak for a rejected provision.
	if s := hh.fake.OwnershipStatus(106); s != "" {
		t.Fatalf("a reservation leaked for a request rejected before reserve: %q", s)
	}
}

func TestProvisionServiceOverQuota409(t *testing.T) {
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
	hh, _ := newCatalogHarness(t, mock)
	tenantID, projID, userID := seedTenant(hh, "contributor")
	hh.fake.AddQuota("tenant", tenantID, iptr(2), nil, nil, nil) // MaxVCPU=2
	hh.fake.AddOwnership(tenantID, projID, 101, "qemu", "pve01", "active", nil)
	c := hh.cookie(t, userID)

	// The service default is 2 vCPU → 2 (used) + 2 > 2 → refused.
	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/service-catalog/postgresql/provision", provisionBody(projID))
	if rec.Code != http.StatusConflict {
		t.Fatalf("over-quota provision = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "quota_exceeded" {
		t.Fatalf("error code = %q, want quota_exceeded", env.Error.Code)
	}
	if s := hh.fake.OwnershipStatus(106); s != "" {
		t.Fatalf("a reservation leaked for a refused provision: %q", s)
	}
}

func TestProvisionServiceCrossTenantProject404(t *testing.T) {
	hh, _ := newCatalogHarness(t, &proxmoxtest.MockClient{})
	tenantID, _, userID := seedTenant(hh, "contributor")
	// A project owned by a DIFFERENT tenant.
	otherTenant := hh.fake.AddTenant("B", "b")
	otherProj := hh.fake.AddProject(otherTenant, "Other", "other", "pc-b-other")
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/service-catalog/postgresql/provision", provisionBody(otherProj))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant project provision = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestProvisionServiceReaderBlocked(t *testing.T) {
	hh, _ := newCatalogHarness(t, &proxmoxtest.MockClient{})
	tenantID, projID, userID := seedTenant(hh, "reader") // Reader, not Contributor
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/service-catalog/postgresql/provision", provisionBody(projID))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reader provision = %d, want 403 (body %s)", rec.Code, rec.Body.String())
	}
}

func TestProvisionServiceUnknownService404(t *testing.T) {
	hh, _ := newCatalogHarness(t, &proxmoxtest.MockClient{})
	tenantID, projID, userID := seedTenant(hh, "contributor")
	c := hh.cookie(t, userID)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/service-catalog/nope/provision", provisionBody(projID))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown service provision = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestServiceCatalogDisabled404: with CATALOG_ENABLED off, the routes are still
// mounted (completeness tests) but the handlers report the feature is not enabled.
func TestServiceCatalogDisabled404(t *testing.T) {
	hh := newHarness(t, &proxmoxtest.MockClient{}) // default harness: catalog off
	tenantID, projID, userID := seedTenant(hh, "contributor")
	c := hh.cookie(t, userID)

	if rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantID+"/service-catalog", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("list (disabled) = %d, want 404", rec.Code)
	}
	if rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantID+"/service-catalog/postgresql/provision", provisionBody(projID)); rec.Code != http.StatusNotFound {
		t.Fatalf("provision (disabled) = %d, want 404", rec.Code)
	}
}
