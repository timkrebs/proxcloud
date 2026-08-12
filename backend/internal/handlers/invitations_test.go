package handlers_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/mail"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

// captureMailer records the last message it was asked to send so invitation
// tests can assert the accept link was delivered (and derive the raw token).
type captureMailer struct {
	mu   sync.Mutex
	sent int
	last mail.Message
}

func (m *captureMailer) Send(_ context.Context, msg mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent++
	m.last = msg
	return nil
}

func (m *captureMailer) snapshot() (int, mail.Message) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sent, m.last
}

// --- CreateInvitation: happy path (201, no token, mail delivered) ---

func TestCreateInvitation201NoTokenAndMailed(t *testing.T) {
	hh, tenantA, c := ownerHarness(t, &proxmoxtest.MockClient{})

	body := `{"email":"New@Example.com","scopeType":"tenant","scopeId":"` + tenantA + `","role":"contributor"}`
	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/invitations", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create invitation = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}

	// The response body carries NO token field.
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, k := range []string{"token", "tokenHash", "token_hash"} {
		if _, ok := raw[k]; ok {
			t.Fatalf("response leaked a token field %q: %v", k, raw)
		}
	}
	got := decodeBody[types.Invitation](t, rec)
	if got.Email != "new@example.com" { // normalized lowercase
		t.Fatalf("email = %q, want normalized new@example.com", got.Email)
	}
	if got.Role != "contributor" || got.Status != "pending" || got.ScopeType != "tenant" {
		t.Fatalf("invitation = %+v, unexpected", got)
	}
	if got.ScopeLabel != "A" {
		t.Fatalf("scopeLabel = %q, want tenant name A", got.ScopeLabel)
	}

	// The mailer received exactly one message with the accept link.
	sent, msg := hh.mailer.snapshot()
	if sent != 1 {
		t.Fatalf("mailer sent = %d, want 1", sent)
	}
	if msg.To != "new@example.com" {
		t.Fatalf("mail To = %q, want new@example.com", msg.To)
	}
	// Extract the token from the accept link and prove only its hash is stored.
	token := extractToken(t, msg.TextBody)
	if token == "" {
		t.Fatalf("no accept link with a token in the mail body:\n%s", msg.TextBody)
	}
	if !strings.Contains(msg.TextBody, "https://portal.test/invite/"+token) {
		t.Fatalf("mail body missing the frontend accept link:\n%s", msg.TextBody)
	}
	invs, err := hh.fake.ListPendingInvitationsByScopes(context.Background(), "tenant", []string{tenantA})
	if err != nil || len(invs) != 1 {
		t.Fatalf("pending invites = %v (err %v), want 1", invs, err)
	}
	wantHash := sha256Hex(token)
	if invs[0].TokenHash != wantHash {
		t.Fatalf("stored token_hash != sha256(token): stored %q", invs[0].TokenHash)
	}
	if invs[0].TokenHash == token {
		t.Fatal("raw token stored — must be hashed")
	}
}

// --- CreateInvitation: cross-tenant project scope → 404 ---

func TestCreateInvitationCrossTenantProjectScope404(t *testing.T) {
	hh, tenantA, c := ownerHarness(t, &proxmoxtest.MockClient{})
	tenantB := hh.fake.AddTenant("B", "b")
	projB := hh.fake.AddProject(tenantB, "Web", "web", "pc-b-web")

	// Owner of A invites into a project that belongs to B → 404 (no existence leak).
	body := `{"email":"x@example.com","scopeType":"project","scopeId":"` + projB + `","role":"reader"}`
	rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/invitations", body)
	wantErrorEnvelope(t, rec, http.StatusNotFound, "not_found")

	if sent, _ := hh.mailer.snapshot(); sent != 0 {
		t.Fatalf("mail sent on a rejected cross-tenant invite = %d, want 0", sent)
	}
}

// --- CreateInvitation: privilege-escalation cap → 403 (direct handler call) ---
//
// The route is Owner-gated, so through the router the inviter is always an Owner
// and the cap can never trip. We drive the handler directly with a synthesized
// Contributor principal to exercise the cap (an Owner cannot be over-granted).
func TestCreateInvitationCapsRoleAtInviter(t *testing.T) {
	fake := storetest.New()
	tenantID := fake.AddTenant("Acme", "acme")
	inviter := fake.AddUser("cara@x.io", "Cara", false)

	d := &handlers.Deps{
		Store:          fake,
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Mailer:         &captureMailer{},
		FrontendOrigin: "https://portal.test",
		InvitationTTL:  time.Hour,
	}
	body := `{"email":"new@x.io","scopeType":"tenant","scopeId":"` + tenantID + `","role":"owner"}`
	req := httptest.NewRequest(http.MethodPost, "/api/tenants/"+tenantID+"/invitations", strings.NewReader(body))
	id := &auth.Identity{UserID: inviter, Email: "cara@x.io", ActiveTenantID: tenantID, EffectiveRole: "contributor"}
	req = req.WithContext(auth.ContextWithIdentity(req.Context(), id))
	rec := httptest.NewRecorder()
	d.CreateInvitation(rec, req)

	wantErrorEnvelope(t, rec, http.StatusForbidden, "forbidden")

	// A contributor CAN, however, grant at-or-below their role (reader).
	body = `{"email":"new2@x.io","scopeType":"tenant","scopeId":"` + tenantID + `","role":"reader"}`
	req = httptest.NewRequest(http.MethodPost, "/api/tenants/"+tenantID+"/invitations", strings.NewReader(body))
	req = req.WithContext(auth.ContextWithIdentity(req.Context(), id))
	rec = httptest.NewRecorder()
	d.CreateInvitation(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("contributor granting reader = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
}

// --- ListInvitations: filtered to the tenant + its projects ---

func TestListInvitationsFilteredByTenant(t *testing.T) {
	hh, tenantA, c := ownerHarness(t, &proxmoxtest.MockClient{})
	projA := hh.fake.AddProject(tenantA, "Web", "web", "pc-a-web")
	tenantB := hh.fake.AddTenant("B", "b")

	inviter := "user-x"
	// One tenant-scope invite for A, one project-scope invite for A's project, and
	// one invite for tenant B that must NOT appear.
	seedInvite(t, hh.fake, "tok-a-tenant", "a@x.io", "tenant", tenantA, "reader", &inviter, time.Now().Add(time.Hour))
	seedInvite(t, hh.fake, "tok-a-proj", "p@x.io", "project", projA, "contributor", &inviter, time.Now().Add(time.Hour))
	seedInvite(t, hh.fake, "tok-b", "b@x.io", "tenant", tenantB, "owner", &inviter, time.Now().Add(time.Hour))

	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/invitations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list invitations = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	list := decodeBody[[]types.Invitation](t, rec)
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2 (tenant B's invite must be filtered out)", len(list))
	}
	emails := map[string]types.Invitation{}
	for _, inv := range list {
		emails[inv.Email] = inv
		if inv.Status != "pending" {
			t.Fatalf("status = %q, want pending", inv.Status)
		}
	}
	if _, ok := emails["b@x.io"]; ok {
		t.Fatal("tenant B's invite leaked into tenant A's list")
	}
	if emails["p@x.io"].ScopeLabel != "A / Web" {
		t.Fatalf("project scope label = %q, want 'A / Web'", emails["p@x.io"].ScopeLabel)
	}
}

// --- ListInvitations: an expired invite is reported as status=expired ---

func TestListInvitationsExpiredStatus(t *testing.T) {
	hh, tenantA, c := ownerHarness(t, &proxmoxtest.MockClient{})
	seedInvite(t, hh.fake, "tok-exp", "old@x.io", "tenant", tenantA, "reader", nil, time.Now().Add(-time.Hour))

	rec := hh.req(t, c, http.MethodGet, "/api/tenants/"+tenantA+"/invitations", "")
	list := decodeBody[[]types.Invitation](t, rec)
	if len(list) != 1 || list[0].Status != "expired" {
		t.Fatalf("list = %+v, want one invite with status expired", list)
	}
}

// --- RevokeInvitation: 204 own, 404 cross-tenant ---

func TestRevokeInvitation(t *testing.T) {
	hh, tenantA, c := ownerHarness(t, &proxmoxtest.MockClient{})
	tenantB := hh.fake.AddTenant("B", "b")

	_, ownInv := seedInvite(t, hh.fake, "tok-own", "a@x.io", "tenant", tenantA, "reader", nil, time.Now().Add(time.Hour))
	_, otherInv := seedInvite(t, hh.fake, "tok-other", "b@x.io", "tenant", tenantB, "reader", nil, time.Now().Add(time.Hour))

	// Revoking another tenant's invite → 404 (and it is NOT deleted).
	rec := hh.req(t, c, http.MethodDelete, "/api/tenants/"+tenantA+"/invitations/"+otherInv.ID, "")
	wantErrorEnvelope(t, rec, http.StatusNotFound, "not_found")
	if invs, _ := hh.fake.ListPendingInvitationsByScopes(context.Background(), "tenant", []string{tenantB}); len(invs) != 1 {
		t.Fatal("cross-tenant revoke deleted another tenant's invite")
	}

	// Revoking own invite → 204 (and it is gone).
	rec = hh.req(t, c, http.MethodDelete, "/api/tenants/"+tenantA+"/invitations/"+ownInv.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke own = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if invs, _ := hh.fake.ListPendingInvitationsByScopes(context.Background(), "tenant", []string{tenantA}); len(invs) != 0 {
		t.Fatal("own invite still present after revoke")
	}

	// Revoking an unknown id → 404.
	rec = hh.req(t, c, http.MethodDelete, "/api/tenants/"+tenantA+"/invitations/does-not-exist", "")
	wantErrorEnvelope(t, rec, http.StatusNotFound, "not_found")
}

// --- CreateInvitation is audited (invitation.create) via AuditOnMutation ---

func TestCreateInvitationAudited(t *testing.T) {
	hh, tenantA, c := ownerHarness(t, &proxmoxtest.MockClient{})
	body := `{"email":"aud@x.io","scopeType":"tenant","scopeId":"` + tenantA + `","role":"reader"}`
	if rec := hh.req(t, c, http.MethodPost, "/api/tenants/"+tenantA+"/invitations", body); rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, want 201 (body %s)", rec.Code, rec.Body.String())
	}
	rows := auditRows(t, hh, tenantA)
	if len(rows) != 1 || rows[0].Action != "invitation.create" || rows[0].Outcome != "success" {
		t.Fatalf("audit rows = %+v, want one invitation.create success", rows)
	}
	if !strings.Contains(string(rows[0].Detail), `"email":"aud@x.io"`) {
		t.Fatalf("audit detail missing annotated email: %s", rows[0].Detail)
	}
}

// --- helpers ---

// seedInvite inserts an invite row (hashed token) and returns (rawToken, row).
func seedInvite(t *testing.T, fake *storetest.Fake, token, email, scopeType, scopeID, role string, invitedBy *string, expiresAt time.Time) (string, *store.Invitation) {
	t.Helper()
	inv, err := fake.CreateInvitation(context.Background(), store.CreateInvitationParams{
		TokenHash: sha256Hex(token),
		Email:     email,
		ScopeType: scopeType,
		ScopeID:   scopeID,
		Role:      role,
		InvitedBy: invitedBy,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		t.Fatalf("seed invite: %v", err)
	}
	return token, inv
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// extractToken pulls the invite token out of an accept link in the mail body.
func extractToken(t *testing.T, body string) string {
	t.Helper()
	const marker = "/invite/"
	i := strings.Index(body, marker)
	if i < 0 {
		return ""
	}
	rest := body[i+len(marker):]
	// The token runs to the first whitespace/newline.
	if j := strings.IndexAny(rest, " \n\r\t"); j >= 0 {
		rest = rest[:j]
	}
	return strings.TrimSpace(rest)
}
