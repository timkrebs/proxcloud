package handlers_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// auditRows reads the fake's tenant-filtered audit spine for assertions.
func auditRows(t *testing.T, hh *harness, tenantID string) []store.AuditEntry {
	t.Helper()
	rows, err := hh.fake.ListAudit(context.Background(), store.AuditQuery{TenantID: tenantID, Limit: 100})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	return rows
}

func ownerHarness(t *testing.T, mock *proxmoxtest.MockClient) (*harness, string, *http.Cookie) {
	t.Helper()
	hh := newHarness(t, mock)
	tenantA := hh.fake.AddTenant("A", "a")
	owner := hh.fake.AddUser("o@x.io", "Ojo", false)
	hh.fake.AddMembership(owner, "tenant", tenantA, "owner")
	return hh, tenantA, hh.cookie(t, owner)
}

// --- a successful mutation leaves EXACTLY ONE audit row, outcome success ---

func TestSuccessfulMutationLeavesOneAuditRow(t *testing.T) {
	mock := &proxmoxtest.MockClient{OnCreatePool: func(context.Context, string, string) error { return nil }}
	hh, tenantA, c := ownerHarness(t, mock)

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/projects", `{"name":"Analytics"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	rows := auditRows(t, hh, tenantA)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want exactly 1", len(rows))
	}
	e := rows[0]
	if e.Action != "project.create" {
		t.Fatalf("audit action = %q, want project.create", e.Action)
	}
	if e.Outcome != "success" {
		t.Fatalf("audit outcome = %q, want success", e.Outcome)
	}
	if e.Detail == nil || !bytes.Contains(e.Detail, []byte(`"status":201`)) {
		t.Fatalf("audit detail = %s, want it to carry status 201", e.Detail)
	}
}

// --- a failed InsertAuditIntent is fail-closed: 500 and NOTHING mutated ---

func TestAuditIntentFailureIsFailClosed(t *testing.T) {
	mock := &proxmoxtest.MockClient{OnCreatePool: func(context.Context, string, string) error {
		t.Fatal("CreatePool must not be called when the audit intent insert fails")
		return nil
	}}
	hh, tenantA, c := ownerHarness(t, mock)
	hh.fake.FailOn("InsertAuditIntent", errors.New("audit db unavailable"))

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/projects", `{"name":"ShouldNotExist"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("intent-failure create = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	if env := decodeBody[types.ErrorEnvelope](t, rec); env.Error.Code != "internal" {
		t.Fatalf("error code = %q, want internal", env.Error.Code)
	}

	// No project was created — the handler never ran.
	projs, err := hh.fake.ListProjectsByTenant(context.Background(), tenantA)
	if err != nil {
		t.Fatalf("ListProjectsByTenant: %v", err)
	}
	if len(projs) != 0 {
		t.Fatalf("projects after fail-closed intent = %d, want 0 (nothing mutated)", len(projs))
	}
	// And no audit row exists (the intent insert failed).
	if rows := auditRows(t, hh, tenantA); len(rows) != 0 {
		t.Fatalf("audit rows after failed intent = %d, want 0", len(rows))
	}
}

// --- a failed FinalizeAudit does NOT 500: status preserved, pending row remains ---

func TestAuditFinalizeFailurePreservesResponse(t *testing.T) {
	var logbuf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logbuf, &slog.HandlerOptions{Level: slog.LevelError}))
	mock := &proxmoxtest.MockClient{OnCreatePool: func(context.Context, string, string) error { return nil }}

	hh := newHarnessLog(t, mock, log)
	tenantA := hh.fake.AddTenant("A", "a")
	owner := hh.fake.AddUser("o@x.io", "Ojo", false)
	hh.fake.AddMembership(owner, "tenant", tenantA, "owner")
	c := hh.cookie(t, owner)
	hh.fake.FailOn("FinalizeAudit", errors.New("audit db blip"))

	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/projects", `{"name":"Analytics"}`)
	// The mutation succeeded and the status is preserved despite the finalize error.
	if rec.Code != http.StatusCreated {
		t.Fatalf("finalize-failure create = %d, want 201 preserved (body %s)", rec.Code, rec.Body.String())
	}

	// The project WAS created (the mutation is real, not rolled back).
	projs, err := hh.fake.ListProjectsByTenant(context.Background(), tenantA)
	if err != nil {
		t.Fatalf("ListProjectsByTenant: %v", err)
	}
	if len(projs) != 1 {
		t.Fatalf("projects = %d, want 1 (mutation committed)", len(projs))
	}
	// The intent row is durable but stays 'pending' (no unlogged mutation).
	rows := auditRows(t, hh, tenantA)
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	if rows[0].Outcome != "pending" {
		t.Fatalf("audit outcome = %q, want pending (finalize failed)", rows[0].Outcome)
	}
	// The failure is loud, not silent.
	if !strings.Contains(logbuf.String(), "audit finalize failed") {
		t.Fatalf("expected a loud finalize-failure log, got:\n%s", logbuf.String())
	}
}
