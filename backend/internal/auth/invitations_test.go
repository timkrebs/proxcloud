package auth

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auditz"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

// errTest is a forced store error for fail-closed coverage.
var errTest = errors.New("forced test error")

// newInviteHandler builds a Handler over storetest.Fake (which records audit rows
// and implements the full invitation/user/membership surface) for the public
// invite validate/accept flow.
func newInviteHandler(t *testing.T) (*Handler, *storetest.Fake) {
	t.Helper()
	fake := storetest.New()
	log := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	h := &Handler{
		Sessions:      NewSessions(fake, false, false, time.Hour, 24*time.Hour),
		Store:         fake,
		Hasher:        NewHasher(),
		Log:           log,
		Limiter:       NewLoginLimiter(),
		Auditz:        &auditz.Recorder{Store: fake, Log: log},
		InvitationTTL: 72 * time.Hour,
	}
	return h, fake
}

// inviteRouter mounts the two public invite routes so chi URL params resolve.
func inviteRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Get("/api/auth/invitations/{token}", h.ValidateInvite)
	r.Post("/api/auth/invitations/{token}/accept", h.AcceptInvite)
	return r
}

// seedInvite inserts an invite row keyed on hashToken(token) and returns it.
func seedInvite(t *testing.T, fake *storetest.Fake, token, email, scopeType, scopeID, role string, invitedBy *string, expiresAt time.Time) *store.Invitation {
	t.Helper()
	inv, err := fake.CreateInvitation(context.Background(), store.CreateInvitationParams{
		TokenHash: hashToken(token),
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
	return inv
}

func getInvite(h *Handler, token string, cookie *http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/invitations/"+token, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	inviteRouter(h).ServeHTTP(rec, req)
	return rec
}

func acceptInvite(h *Handler, token, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := jsonReq(http.MethodPost, "/api/auth/invitations/"+token+"/accept", body)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	inviteRouter(h).ServeHTTP(rec, req)
	return rec
}

// --- ValidateInvite: enumeration-safe generic 404 ---

func TestValidateInviteGeneric404(t *testing.T) {
	h, fake := newInviteHandler(t)
	tenantID := fake.AddTenant("Acme", "acme")
	seedInvite(t, fake, "expired-tok", "e@x.io", "tenant", tenantID, "reader", nil, time.Now().Add(-time.Hour))
	accepted := seedInvite(t, fake, "used-tok", "u@x.io", "tenant", tenantID, "reader", nil, time.Now().Add(time.Hour))
	if ok, _ := fake.MarkInvitationAccepted(context.Background(), accepted.ID); !ok {
		t.Fatal("failed to mark invite accepted for setup")
	}

	for _, token := range []string{"unknown-tok", "expired-tok", "used-tok"} {
		rec := getInvite(h, token, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("token %q status = %d, want 404 (body %s)", token, rec.Code, rec.Body.String())
		}
		var env types.ErrorEnvelope
		_ = json.Unmarshal(rec.Body.Bytes(), &env)
		if env.Error.Code != "not_found" {
			t.Fatalf("token %q code = %q, want not_found", token, env.Error.Code)
		}
	}
}

// --- ValidateInvite: RequiresAccount + SignedInMatches ---

func TestValidateInviteDetails(t *testing.T) {
	h, fake := newInviteHandler(t)
	tenantID := fake.AddTenant("Acme", "acme")
	projID := fake.AddProject(tenantID, "Web", "web", "pc-acme-web")
	seedInvite(t, fake, "proj-tok", "invitee@x.io", "project", projID, "contributor", nil, time.Now().Add(time.Hour))

	// No user for the email yet → RequiresAccount, not signed in.
	rec := getInvite(h, "proj-tok", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("validate = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var d types.InvitationDetails
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !d.RequiresAccount || d.SignedInMatches {
		t.Fatalf("details = %+v, want RequiresAccount and !SignedInMatches", d)
	}
	if d.TenantName != "Acme" || d.ScopeLabel != "Acme / Web" || d.Role != "contributor" || d.Email != "invitee@x.io" {
		t.Fatalf("details = %+v, unexpected labels", d)
	}

	// Now create + sign in AS the invite email → !RequiresAccount, SignedInMatches.
	uid := fake.AddUser("invitee@x.io", "Inv", false)
	cookie, err := h.Sessions.Issue(context.Background(), uid, httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	rec = getInvite(h, "proj-tok", cookie)
	_ = json.Unmarshal(rec.Body.Bytes(), &d)
	if d.RequiresAccount || !d.SignedInMatches {
		t.Fatalf("signed-in details = %+v, want !RequiresAccount and SignedInMatches", d)
	}
}

// --- AcceptInvite: create a new account, membership FROM THE ROW ---

func TestAcceptInviteCreateNew(t *testing.T) {
	h, fake := newInviteHandler(t)
	tenantID := fake.AddTenant("Acme", "acme")
	inv := seedInvite(t, fake, "new-tok", "new@x.io", "tenant", tenantID, "reader", nil, time.Now().Add(time.Hour))

	rec := acceptInvite(h, "new-tok", `{"displayName":"New User","password":"a-strong-passphrase"}`, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("accept = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	if !hasSessionCookie(rec) {
		t.Fatal("accept did not set a session cookie")
	}

	u, err := fake.GetUserByEmail(context.Background(), "new@x.io")
	if err != nil {
		t.Fatalf("user not created: %v", err)
	}
	mems, _ := fake.ListMembershipsByUser(context.Background(), u.ID)
	if len(mems) != 1 || mems[0].Role != "reader" || mems[0].ScopeType != "tenant" || mems[0].ScopeID != tenantID {
		t.Fatalf("membership = %+v, want single tenant reader from the row", mems)
	}
	// The invite is now single-use consumed.
	got, _ := fake.GetInvitationByTokenHash(context.Background(), hashToken("new-tok"))
	if got == nil || got.AcceptedAt == nil {
		t.Fatalf("invite not stamped accepted: %+v", got)
	}
	_ = inv

	// The issued session's active tenant is the invite tenant.
	id, verr := h.Sessions.Verify(context.Background(), reqWithCookie(rec))
	if verr != nil {
		t.Fatalf("verify issued session: %v", verr)
	}
	if id.ActiveTenantID != tenantID {
		t.Fatalf("active tenant = %q, want %q", id.ActiveTenantID, tenantID)
	}
}

// --- AcceptInvite: attach an already signed-in user whose email matches ---

func TestAcceptInviteAttachSignedIn(t *testing.T) {
	h, fake := newInviteHandler(t)
	tenantID := fake.AddTenant("Acme", "acme")
	seedInvite(t, fake, "attach-tok", "member@x.io", "tenant", tenantID, "contributor", nil, time.Now().Add(time.Hour))

	uid := fake.AddUser("member@x.io", "Mem", false)
	cookie, _ := h.Sessions.Issue(context.Background(), uid, httptest.NewRequest(http.MethodGet, "/", nil))

	rec := acceptInvite(h, "attach-tok", `{}`, cookie)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("attach accept = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	mems, _ := fake.ListMembershipsByUser(context.Background(), uid)
	if len(mems) != 1 || mems[0].Role != "contributor" {
		t.Fatalf("membership = %+v, want one contributor grant to the signed-in user", mems)
	}
	// No new user was created for that email.
	users, _ := fake.ListUsersByIDs(context.Background(), []string{uid})
	if len(users) != 1 {
		t.Fatalf("user count for email unexpectedly changed: %v", users)
	}
}

// --- AcceptInvite: signed in as a different email → 409 email_mismatch ---

func TestAcceptInviteEmailMismatch(t *testing.T) {
	h, fake := newInviteHandler(t)
	tenantID := fake.AddTenant("Acme", "acme")
	seedInvite(t, fake, "mm-tok", "target@x.io", "tenant", tenantID, "reader", nil, time.Now().Add(time.Hour))

	other := fake.AddUser("someone-else@x.io", "Else", false)
	cookie, _ := h.Sessions.Issue(context.Background(), other, httptest.NewRequest(http.MethodGet, "/", nil))

	rec := acceptInvite(h, "mm-tok", `{}`, cookie)
	if rec.Code != http.StatusConflict {
		t.Fatalf("mismatch accept = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var env types.ErrorEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != "email_mismatch" {
		t.Fatalf("code = %q, want email_mismatch", env.Error.Code)
	}
	// No membership was granted to the mismatched user.
	if mems, _ := fake.ListMembershipsByUser(context.Background(), other); len(mems) != 0 {
		t.Fatalf("mismatched user gained a membership: %+v", mems)
	}
}

// --- AcceptInvite: existing account, not signed in → 409 account_exists ---

func TestAcceptInviteAccountExists(t *testing.T) {
	h, fake := newInviteHandler(t)
	tenantID := fake.AddTenant("Acme", "acme")
	seedInvite(t, fake, "ae-tok", "exists@x.io", "tenant", tenantID, "reader", nil, time.Now().Add(time.Hour))
	uid := fake.AddUser("exists@x.io", "Exists", false)

	rec := acceptInvite(h, "ae-tok", `{"displayName":"X","password":"a-strong-passphrase"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("account_exists accept = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var env types.ErrorEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != "account_exists" {
		t.Fatalf("code = %q, want account_exists", env.Error.Code)
	}
	if mems, _ := fake.ListMembershipsByUser(context.Background(), uid); len(mems) != 0 {
		t.Fatalf("existing user gained a membership without signing in: %+v", mems)
	}
}

// --- AcceptInvite: a raced CreateUser unique-email conflict → 409, not 500 ---
//
// The pre-tx GetUserByEmail misses (no account), so the handler takes the
// create-new-user path; a concurrent accept then creates the account for the
// same email between that read and the in-tx CreateUser, tripping the unique
// constraint (store.ErrConflict). That must map to 409 account_exists, never 500.
func TestAcceptInviteRacedCreateUserConflict(t *testing.T) {
	h, fake := newInviteHandler(t)
	tenantID := fake.AddTenant("Acme", "acme")
	seedInvite(t, fake, "race-tok", "race@x.io", "tenant", tenantID, "reader", nil, time.Now().Add(time.Hour))

	// Force the in-tx CreateUser to lose the unique-email race.
	fake.FailOn("CreateUser", store.ErrConflict)

	rec := acceptInvite(h, "race-tok", `{"displayName":"R","password":"a-strong-passphrase"}`, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("raced create accept = %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
	var env types.ErrorEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != "account_exists" {
		t.Fatalf("code = %q, want account_exists", env.Error.Code)
	}
	// The invite was not consumed (the tx rolled back before the single-use stamp).
	got, _ := fake.GetInvitationByTokenHash(context.Background(), hashToken("race-tok"))
	if got == nil || got.AcceptedAt != nil {
		t.Fatalf("invite consumed despite the raced-create rollback: %+v", got)
	}
}

// --- AcceptInvite: role comes from the row, not any client input ---

func TestAcceptInviteRoleFromRowNotBody(t *testing.T) {
	h, fake := newInviteHandler(t)
	tenantID := fake.AddTenant("Acme", "acme")
	seedInvite(t, fake, "role-tok", "role@x.io", "tenant", tenantID, "reader", nil, time.Now().Add(time.Hour))

	// The accept body has no role field; even a client that smuggled "role":"owner"
	// cannot change the grant — the membership role is bound in the invite row.
	rec := acceptInvite(h, "role-tok", `{"displayName":"R","password":"a-strong-passphrase","role":"owner"}`, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("accept = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	u, _ := fake.GetUserByEmail(context.Background(), "role@x.io")
	mems, _ := fake.ListMembershipsByUser(context.Background(), u.ID)
	if len(mems) != 1 || mems[0].Role != "reader" {
		t.Fatalf("granted role = %+v, want reader (from the row, not the body)", mems)
	}
}

// --- AcceptInvite: double-accept grants no second membership ---

func TestAcceptInviteDoubleAcceptNoSecondMembership(t *testing.T) {
	h, fake := newInviteHandler(t)
	tenantID := fake.AddTenant("Acme", "acme")
	seedInvite(t, fake, "dbl-tok", "dbl@x.io", "tenant", tenantID, "reader", nil, time.Now().Add(time.Hour))

	if rec := acceptInvite(h, "dbl-tok", `{"displayName":"D","password":"a-strong-passphrase"}`, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("first accept = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	u, _ := fake.GetUserByEmail(context.Background(), "dbl@x.io")

	// Second accept with the same (now consumed) token → generic 404.
	rec := acceptInvite(h, "dbl-tok", `{"displayName":"D","password":"a-strong-passphrase"}`, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second accept = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	mems, _ := fake.ListMembershipsByUser(context.Background(), u.ID)
	if len(mems) != 1 {
		t.Fatalf("memberships after double-accept = %d, want exactly 1", len(mems))
	}
}

// --- AcceptInvite: one audit row is written on success ---

func TestAcceptInviteAudited(t *testing.T) {
	h, fake := newInviteHandler(t)
	tenantID := fake.AddTenant("Acme", "acme")
	seedInvite(t, fake, "aud-tok", "aud@x.io", "tenant", tenantID, "reader", nil, time.Now().Add(time.Hour))

	if rec := acceptInvite(h, "aud-tok", `{"displayName":"A","password":"a-strong-passphrase"}`, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("accept = %d, want 204 (body %s)", rec.Code, rec.Body.String())
	}
	var found *store.AuditEntry
	for _, e := range fake.AllAudit() {
		if e.Action == "invitation.accept" {
			ec := e
			found = &ec
			break
		}
	}
	if found == nil {
		t.Fatal("no invitation.accept audit row written")
	}
	if found.Outcome != "success" {
		t.Fatalf("audit outcome = %q, want success", found.Outcome)
	}
	if found.TenantID == nil || *found.TenantID != tenantID {
		t.Fatalf("audit tenant = %v, want %q", found.TenantID, tenantID)
	}
}

// --- AcceptInvite: a forced audit-intent failure is fail-closed ---

func TestAcceptInviteFailClosedOnIntentFailure(t *testing.T) {
	h, fake := newInviteHandler(t)
	tenantID := fake.AddTenant("Acme", "acme")
	seedInvite(t, fake, "fc-tok", "fc@x.io", "tenant", tenantID, "reader", nil, time.Now().Add(time.Hour))
	fake.FailOn("InsertAuditIntent", errTest)

	rec := acceptInvite(h, "fc-tok", `{"displayName":"F","password":"a-strong-passphrase"}`, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("intent-failure accept = %d, want 500 (body %s)", rec.Code, rec.Body.String())
	}
	// Nothing was mutated: no user, no membership, invite still pending.
	if _, err := fake.GetUserByEmail(context.Background(), "fc@x.io"); err == nil {
		t.Fatal("a user was created despite the fail-closed intent")
	}
	got, _ := fake.GetInvitationByTokenHash(context.Background(), hashToken("fc-tok"))
	if got == nil || got.AcceptedAt != nil {
		t.Fatalf("invite was consumed despite fail-closed intent: %+v", got)
	}
}

// reqWithCookie builds a bare GET request carrying the session cookie set on rec.
func reqWithCookie(rec *httptest.ResponseRecorder) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName {
			req.AddCookie(c)
		}
	}
	return req
}
