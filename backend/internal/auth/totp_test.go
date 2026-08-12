package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pquerna/otp/totp"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auditz"
	"github.com/timkrebs9/proxcloud/backend/internal/secrets"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// newTOTPHandler builds a Handler wired with a real secrets cipher and an auditz
// recorder over the package fakeStore (which stores passwords, flips
// totp_enabled, and now records audit rows) — the full surface chunk C needs.
func newTOTPHandler(t *testing.T) (*Handler, *fakeStore) {
	t.Helper()
	fs := newFakeStore()
	cipher, err := secrets.New(bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatalf("secrets.New: %v", err)
	}
	log := slog.New(slog.NewTextHandler(testWriter{t}, nil))
	h := &Handler{
		Sessions:          NewSessions(fs, false, time.Hour, 24*time.Hour),
		Store:             fs,
		Hasher:            NewHasher(),
		Log:               log,
		Limiter:           NewLoginLimiter(),
		Secrets:           cipher,
		Auditz:            &auditz.Recorder{Store: fs, Log: log},
		LoginChallengeTTL: 5 * time.Minute,
		TOTPIssuer:        "Proxcloud",
	}
	return h, fs
}

// totpRouter mounts the public login routes and the authenticated account
// surface so chi resolves URL params and the Authenticate gate runs.
func totpRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	r.Post("/api/auth/login", h.Login)
	r.Post("/api/auth/login/totp", h.LoginTOTP)
	r.Group(func(r chi.Router) {
		r.Use(h.Authenticate)
		r.Get("/api/auth/me", h.Me)
		r.Post("/api/auth/password", h.ChangePassword)
		r.Post("/api/auth/totp/enroll", h.EnrollTOTP)
		r.Post("/api/auth/totp/verify", h.VerifyEnrollTOTP)
		r.Post("/api/auth/totp/disable", h.DisableTOTP)
		r.Post("/api/auth/totp/recovery-codes", h.RegenerateRecoveryCodes)
	})
	return r
}

func getAuthed(r chi.Router, target string, cookie *http.Cookie) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	r.ServeHTTP(rec, req)
	return rec
}

// enroll POSTs /totp/enroll and returns the parsed response.
func enroll(t *testing.T, h *Handler, cookie *http.Cookie) types.EnrollTOTPResponse {
	t.Helper()
	rec := postAuthed(totpRouter(h), "/api/auth/totp/enroll", cookie, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enroll = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var out types.EnrollTOTPResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("enroll decode: %v", err)
	}
	return out
}

// enableTOTP runs the full enroll→verify flow and returns the manual base32 key
// plus the 10 recovery codes shown once.
func enableTOTP(t *testing.T, h *Handler, cookie *http.Cookie) (manualKey string, codes []string) {
	t.Helper()
	en := enroll(t, h, cookie)
	code, err := totp.GenerateCode(en.ManualKey, time.Now())
	if err != nil {
		t.Fatalf("GenerateCode: %v", err)
	}
	rec := postAuthed(totpRouter(h), "/api/auth/totp/verify", cookie, `{"code":"`+code+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var vr types.VerifyEnrollResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &vr); err != nil {
		t.Fatalf("verify decode: %v", err)
	}
	return en.ManualKey, vr.RecoveryCodes
}

func challengeCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == ChallengeCookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

// --- enroll → verify happy path + secret-at-rest security ---

func TestTOTPEnrollVerifyHappy(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	cookie := issueCookie(t, h, u.ID)

	en := enroll(t, h, cookie)
	if en.OtpauthURI == "" || !strings.HasPrefix(en.QrPngDataUri, "data:image/png;base64,") || en.ManualKey == "" {
		t.Fatalf("enroll response incomplete: %+v", en)
	}

	// Secret at rest is AES-256-GCM ciphertext: the plaintext base32 never
	// appears in the stored column, and a flipped byte fails Open (fail-closed).
	sec, err := fs.GetTOTPSecret(context.Background(), u.ID)
	if err != nil {
		t.Fatalf("GetTOTPSecret: %v", err)
	}
	if bytes.Contains(sec.SecretEncrypted, []byte(en.ManualKey)) {
		t.Fatal("plaintext base32 secret found in secret_encrypted — not encrypted at rest")
	}
	if plain, oerr := h.Secrets.Open(sec.SecretEncrypted); oerr != nil || string(plain) != en.ManualKey {
		t.Fatalf("Open(sealed) = %q, err %v; want %q", plain, oerr, en.ManualKey)
	}
	tampered := append([]byte(nil), sec.SecretEncrypted...)
	tampered[len(tampered)-1] ^= 0x01
	if _, oerr := h.Secrets.Open(tampered); oerr == nil {
		t.Fatal("Open(tampered) succeeded — GCM did not fail closed")
	}
	// Not yet enabled: enroll alone changes no security state.
	if uu := mustUser(t, fs, u.ID); uu.TOTPEnabled {
		t.Fatal("totp_enabled flipped by enroll alone")
	}

	// Verify with a live code enables TOTP and returns 10 recovery codes once.
	code, _ := totp.GenerateCode(en.ManualKey, time.Now())
	rec := postAuthed(totpRouter(h), "/api/auth/totp/verify", cookie, `{"code":"`+code+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var vr types.VerifyEnrollResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &vr)
	if len(vr.RecoveryCodes) != 10 {
		t.Fatalf("recovery codes = %d, want 10", len(vr.RecoveryCodes))
	}
	if !mustUser(t, fs, u.ID).TOTPEnabled {
		t.Fatal("totp_enabled did not flip after verify")
	}
	if n, _ := fs.CountUnusedRecoveryCodes(context.Background(), u.ID); n != 10 {
		t.Fatalf("unused recovery codes = %d, want 10", n)
	}
	// totp.enable was audited exactly once, success.
	assertAudited(t, fs, "totp.enable", "success")
}

// --- verify: bad code → 401, no enable ---

func TestTOTPVerifyBadCode(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	cookie := issueCookie(t, h, u.ID)
	_ = enroll(t, h, cookie)

	rec := postAuthed(totpRouter(h), "/api/auth/totp/verify", cookie, `{"code":"000000"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad-code verify = %d, want 401 (%s)", rec.Code, rec.Body)
	}
	if mustUser(t, fs, u.ID).TOTPEnabled {
		t.Fatal("totp_enabled flipped despite bad code")
	}

	// No pending secret → 409.
	other := seedUser(t, h, fs, "other@b.com", "another-strong-pass", false)
	rec = postAuthed(totpRouter(h), "/api/auth/totp/verify", issueCookie(t, h, other.ID), `{"code":"000000"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("verify with no pending secret = %d, want 409", rec.Code)
	}
}

// --- enroll when already enabled → 409 ---

func TestTOTPEnrollConflictWhenEnabled(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	cookie := issueCookie(t, h, u.ID)
	enableTOTP(t, h, cookie)

	rec := postAuthed(totpRouter(h), "/api/auth/totp/enroll", cookie, `{}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-enroll while enabled = %d, want 409 (%s)", rec.Code, rec.Body)
	}
}

// --- disable: wrong password 401; right password clears secret + codes ---

func TestTOTPDisable(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	cookie := issueCookie(t, h, u.ID)
	enableTOTP(t, h, cookie)

	// wrong password → 401, still enabled.
	rec := postAuthed(totpRouter(h), "/api/auth/totp/disable", cookie, `{"password":"nope"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("disable wrong password = %d, want 401 (%s)", rec.Code, rec.Body)
	}
	if !mustUser(t, fs, u.ID).TOTPEnabled {
		t.Fatal("totp disabled on wrong password")
	}

	// right password → 204, secret + codes gone, flag cleared.
	rec = postAuthed(totpRouter(h), "/api/auth/totp/disable", cookie, `{"password":"correct-horse-battery"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("disable = %d, want 204 (%s)", rec.Code, rec.Body)
	}
	if mustUser(t, fs, u.ID).TOTPEnabled {
		t.Fatal("totp_enabled still set after disable")
	}
	if _, err := fs.GetTOTPSecret(context.Background(), u.ID); err == nil {
		t.Fatal("totp secret not deleted on disable")
	}
	if n, _ := fs.CountUnusedRecoveryCodes(context.Background(), u.ID); n != 0 {
		t.Fatalf("recovery codes after disable = %d, want 0", n)
	}
	assertAudited(t, fs, "totp.disable", "success")
}

// --- regenerate: re-verify + 409 when disabled ---

func TestTOTPRegenerateRecoveryCodes(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	cookie := issueCookie(t, h, u.ID)

	// 409 while TOTP is disabled (even with the correct password).
	rec := postAuthed(totpRouter(h), "/api/auth/totp/recovery-codes", cookie, `{"password":"correct-horse-battery"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("regenerate while disabled = %d, want 409 (%s)", rec.Code, rec.Body)
	}

	_, codes := enableTOTP(t, h, cookie)

	// wrong password → 401.
	rec = postAuthed(totpRouter(h), "/api/auth/totp/recovery-codes", cookie, `{"password":"nope"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("regenerate wrong password = %d, want 401", rec.Code)
	}

	// right password → 200 with 10 fresh codes distinct from the originals.
	rec = postAuthed(totpRouter(h), "/api/auth/totp/recovery-codes", cookie, `{"password":"correct-horse-battery"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("regenerate = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var rr types.RecoveryCodesResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &rr)
	if len(rr.RecoveryCodes) != 10 {
		t.Fatalf("regenerated codes = %d, want 10", len(rr.RecoveryCodes))
	}
	// An old code no longer validates after regenerate (single-use login proves consume).
	if hashRecoveryCode(codes[0]) == hashRecoveryCode(rr.RecoveryCodes[0]) {
		t.Fatal("regenerate returned an identical first code")
	}
	assertAudited(t, fs, "recovery.regenerate", "success")
}

// --- Me exposes the unused recovery-code count, never the codes ---

func TestMeRecoveryCodesRemaining(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	cookie := issueCookie(t, h, u.ID)
	enableTOTP(t, h, cookie)

	rec := getAuthed(totpRouter(h), "/api/auth/me", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("me = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var me types.Me
	_ = json.Unmarshal(rec.Body.Bytes(), &me)
	if me.RecoveryCodesRemaining != 10 {
		t.Fatalf("recoveryCodesRemaining = %d, want 10", me.RecoveryCodesRemaining)
	}
	if !me.TOTPEnabled {
		t.Fatal("me.totpEnabled false after enable")
	}
	if strings.Contains(rec.Body.String(), "XXXXX") || strings.Contains(strings.ToLower(rec.Body.String()), "recoverycodes\"") {
		t.Fatal("Me leaked recovery codes")
	}
}

// --- login with TOTP enabled → {totpRequired:true}, no session, a challenge row ---

func TestLoginTOTPRequiredMintsChallenge(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	enableTOTP(t, h, issueCookie(t, h, u.ID))

	rec := postWithCookie(h, "/api/auth/login", nil, `{"email":"user@b.com","password":"correct-horse-battery"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var lr types.LoginResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &lr)
	if !lr.TotpRequired {
		t.Fatal("login did not report totpRequired")
	}
	if hasSessionCookie(rec) {
		t.Fatal("login set a session cookie despite TOTP being required")
	}
	cc := challengeCookie(rec)
	if cc == nil {
		t.Fatal("login did not set a proxcloud_totp challenge cookie")
	}
	if _, err := fs.GetLoginChallengeByTokenHash(context.Background(), hashToken(cc.Value)); err != nil {
		t.Fatalf("no challenge row for the issued cookie: %v", err)
	}
}

// --- login/totp: valid TOTP → 204 + session; reuse of the same challenge fails ---

func TestLoginTOTPValidThenChallengeSingleUse(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	manualKey, _ := enableTOTP(t, h, issueCookie(t, h, u.ID))

	login := postWithCookie(h, "/api/auth/login", nil, `{"email":"user@b.com","password":"correct-horse-battery"}`)
	cc := challengeCookie(login)
	if cc == nil {
		t.Fatal("no challenge cookie from login")
	}

	code, _ := totp.GenerateCode(manualKey, time.Now())
	rec := postWithCookie(h, "/api/auth/login/totp", cc, `{"code":"`+code+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("login/totp = %d, want 204 (%s)", rec.Code, rec.Body)
	}
	if !hasSessionCookie(rec) {
		t.Fatal("login/totp did not set a session cookie")
	}
	// The session works against a protected route.
	if got := getAuthed(totpRouter(h), "/api/auth/me", sessionCookie(rec)); got.Code != http.StatusOK {
		t.Fatalf("me with issued session = %d, want 200", got.Code)
	}
	assertAudited(t, fs, "totp.login", "success")

	// Re-using the SAME (now consumed) challenge cookie fails as expired.
	code2, _ := totp.GenerateCode(manualKey, time.Now())
	reuse := postWithCookie(h, "/api/auth/login/totp", cc, `{"code":"`+code2+`"}`)
	if reuse.Code != http.StatusUnauthorized {
		t.Fatalf("reuse of consumed challenge = %d, want 401", reuse.Code)
	}
	assertErrCode(t, reuse, "totp_challenge_expired")
}

// --- login/totp: a recovery code logs in once, then is spent ---

func TestLoginTOTPRecoveryCodeSingleUse(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	_, codes := enableTOTP(t, h, issueCookie(t, h, u.ID))

	// First use: a recovery code logs in.
	login := postWithCookie(h, "/api/auth/login", nil, `{"email":"user@b.com","password":"correct-horse-battery"}`)
	rec := postWithCookie(h, "/api/auth/login/totp", challengeCookie(login), `{"code":"`+codes[0]+`"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("recovery login = %d, want 204 (%s)", rec.Code, rec.Body)
	}
	if n, _ := fs.CountUnusedRecoveryCodes(context.Background(), u.ID); n != 9 {
		t.Fatalf("unused codes after one recovery login = %d, want 9", n)
	}

	// Second use of the SAME code (fresh challenge) is rejected.
	login2 := postWithCookie(h, "/api/auth/login", nil, `{"email":"user@b.com","password":"correct-horse-battery"}`)
	rec2 := postWithCookie(h, "/api/auth/login/totp", challengeCookie(login2), `{"code":"`+codes[0]+`"}`)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("reused recovery code = %d, want 401", rec2.Code)
	}
	assertErrCode(t, rec2, "unauthenticated")
}

// --- login/totp: attempts increment and the challenge locks at the 5th failure ---

func TestLoginTOTPLockoutAtFive(t *testing.T) {
	h, fs := newTOTPHandler(t)
	h.Limiter = nil // isolate the per-account challenge lockout from the per-IP cap
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	enableTOTP(t, h, issueCookie(t, h, u.ID))

	login := postWithCookie(h, "/api/auth/login", nil, `{"email":"user@b.com","password":"correct-horse-battery"}`)
	cc := challengeCookie(login)

	// Four wrong codes → 401 unauthenticated, challenge alive.
	for i := 1; i <= 4; i++ {
		rec := postWithCookie(h, "/api/auth/login/totp", cc, `{"code":"000000"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("attempt %d = %d, want 401", i, rec.Code)
		}
		assertErrCode(t, rec, "unauthenticated")
		ch, _ := fs.GetLoginChallengeByTokenHash(context.Background(), hashToken(cc.Value))
		if ch.Attempts != i {
			t.Fatalf("attempts after failure %d = %d, want %d", i, ch.Attempts, i)
		}
	}

	// Fifth wrong code → challenge self-consumes → totp_challenge_expired.
	rec := postWithCookie(h, "/api/auth/login/totp", cc, `{"code":"000000"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("fifth attempt = %d, want 401", rec.Code)
	}
	assertErrCode(t, rec, "totp_challenge_expired")
	ch, _ := fs.GetLoginChallengeByTokenHash(context.Background(), hashToken(cc.Value))
	if ch.ConsumedAt == nil {
		t.Fatal("challenge not consumed after 5 failures")
	}
}

// --- login/totp: an expired challenge is rejected ---

func TestLoginTOTPExpiredChallenge(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	enableTOTP(t, h, issueCookie(t, h, u.ID))

	token, _ := mintChallengeToken()
	if _, err := fs.CreateLoginChallenge(context.Background(), store.CreateLoginChallengeParams{
		UserID:    u.ID,
		TokenHash: hashToken(token),
		ExpiresAt: time.Now().Add(-time.Minute),
	}); err != nil {
		t.Fatalf("seed challenge: %v", err)
	}
	rec := postWithCookie(h, "/api/auth/login/totp", &http.Cookie{Name: ChallengeCookieName, Value: token}, `{"code":"000000"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired challenge = %d, want 401", rec.Code)
	}
	assertErrCode(t, rec, "totp_challenge_expired")

	// A missing challenge cookie is likewise rejected.
	rec = postWithCookie(h, "/api/auth/login/totp", nil, `{"code":"000000"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing challenge = %d, want 401", rec.Code)
	}
	assertErrCode(t, rec, "totp_challenge_expired")
}

// --- the proxcloud_totp challenge cookie is NOT a session on any protected route ---

func TestChallengeCookieIsNotASession(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	enableTOTP(t, h, issueCookie(t, h, u.ID))

	// A real, live challenge — the kind login sets after a correct password.
	login := postWithCookie(h, "/api/auth/login", nil, `{"email":"user@b.com","password":"correct-horse-battery"}`)
	cc := challengeCookie(login)
	if cc == nil {
		t.Fatal("expected a challenge cookie")
	}

	protected := []struct {
		method, path string
	}{
		{http.MethodGet, "/api/auth/me"},
		{http.MethodPost, "/api/auth/password"},
		{http.MethodPost, "/api/auth/totp/enroll"},
		{http.MethodPost, "/api/auth/totp/verify"},
		{http.MethodPost, "/api/auth/totp/disable"},
		{http.MethodPost, "/api/auth/totp/recovery-codes"},
	}
	for _, p := range protected {
		rec := httptest.NewRecorder()
		req := jsonReq(p.method, p.path, `{}`)
		req.AddCookie(cc) // ONLY the challenge cookie, never a session cookie
		totpRouter(h).ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s with only proxcloud_totp = %d, want 401", p.method, p.path, rec.Code)
		}
	}
}

// --- password.change is audited (closes the Phase-4 gap) ---

func TestChangePasswordAudited(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "original-password-1", false)
	cookie := issueCookie(t, h, u.ID)

	rec := postAuthed(totpRouter(h), "/api/auth/password", cookie,
		`{"currentPassword":"original-password-1","newPassword":"a-brand-new-passphrase"}`)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("change password = %d, want 204 (%s)", rec.Code, rec.Body)
	}
	assertAudited(t, fs, "password.change", "success")
}

// --- totp.enable is fail-closed on a forced audit-intent failure ---

func TestTOTPEnableFailClosedOnIntentFailure(t *testing.T) {
	h, fs := newTOTPHandler(t)
	u := seedUser(t, h, fs, "user@b.com", "correct-horse-battery", false)
	cookie := issueCookie(t, h, u.ID)
	en := enroll(t, h, cookie)
	fs.failOn("InsertAuditIntent", errTest)

	code, _ := totp.GenerateCode(en.ManualKey, time.Now())
	rec := postAuthed(totpRouter(h), "/api/auth/totp/verify", cookie, `{"code":"`+code+`"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("verify with forced intent failure = %d, want 500 (%s)", rec.Code, rec.Body)
	}
	// Fail-closed: nothing was enabled and no recovery codes were minted.
	if mustUser(t, fs, u.ID).TOTPEnabled {
		t.Fatal("totp enabled despite fail-closed intent")
	}
	if n, _ := fs.CountUnusedRecoveryCodes(context.Background(), u.ID); n != 0 {
		t.Fatalf("recovery codes minted despite fail-closed intent: %d", n)
	}
}

// --- recovery-code normalization (Crockford look-alike folding) ---

func TestRecoveryCodeNormalization(t *testing.T) {
	// Hyphen/space stripping, casing, and I/L→1, O→0 folding all hash the same.
	base := hashRecoveryCode("ABCDE-01234")
	for _, variant := range []string{"abcde-01234", "ABCDE 01234", "ABCDE01234", "abcde 0 1 2 3 4"} {
		if hashRecoveryCode(variant) != base {
			t.Fatalf("variant %q did not normalize to the canonical hash", variant)
		}
	}
	if hashRecoveryCode("IL0O") != hashRecoveryCode("1100") {
		t.Fatal("Crockford I/L→1, O→0 folding not applied")
	}
	// genRecoveryCodes shape: XXXXX-XXXXX, 10 codes.
	codes, err := genRecoveryCodes(10)
	if err != nil {
		t.Fatalf("genRecoveryCodes: %v", err)
	}
	if len(codes) != 10 {
		t.Fatalf("gen count = %d, want 10", len(codes))
	}
	for _, c := range codes {
		if len(c) != 11 || c[5] != '-' {
			t.Fatalf("code %q is not XXXXX-XXXXX", c)
		}
	}
}

// --- helpers ---

// postWithCookie POSTs a JSON body to the public login/totp route carrying an
// arbitrary cookie (the challenge cookie), through the real router.
func postWithCookie(h *Handler, target string, cookie *http.Cookie, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := jsonReq(http.MethodPost, target, body)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	totpRouter(h).ServeHTTP(rec, req)
	return rec
}

func sessionCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == CookieName && c.Value != "" {
			return c
		}
	}
	return nil
}

func assertErrCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var env types.ErrorEnvelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != want {
		t.Fatalf("error code = %q, want %q (%s)", env.Error.Code, want, rec.Body)
	}
}

// assertAudited asserts exactly one audit row for action exists with the given
// outcome (mirrors the invitation.accept audit test).
func assertAudited(t *testing.T, fs *fakeStore, action, outcome string) {
	t.Helper()
	var found *store.AuditEntry
	n := 0
	for _, e := range fs.allAudit() {
		if e.Action == action {
			n++
			ec := e
			found = &ec
		}
	}
	if n != 1 {
		t.Fatalf("audit rows for %q = %d, want 1", action, n)
	}
	if found.Outcome != outcome {
		t.Fatalf("audit %q outcome = %q, want %q", action, found.Outcome, outcome)
	}
}
