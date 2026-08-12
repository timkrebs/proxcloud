package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image/png"
	"net/http"
	"strings"

	"github.com/pquerna/otp/totp"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auditz"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

const (
	// recoveryCodeCount is how many single-use recovery codes are minted at
	// TOTP-enable and at each regenerate (ADR-0013 §4).
	recoveryCodeCount = 10
	// recoveryCodeSymbols is the length of each half of an "XXXXX-XXXXX" code.
	recoveryCodeHalf = 5
	// maxTOTPAttempts is the per-challenge failure budget; the challenge
	// self-consumes on the 5th failure, forcing a fresh password entry.
	maxTOTPAttempts = 5
	// challengeTokenBytes is the interim login-challenge token length (256 bits).
	challengeTokenBytes = 32
	// crockford is the Crockford base32 alphabet (I, L, O, U excluded) used for
	// recovery codes. It has 32 symbols, so a random byte & 0x1F maps uniformly
	// (256 is an exact multiple of 32 — no modulo bias).
	crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
)

// defaultTOTPIssuer is used when TOTP_ISSUER is unset (pquerna requires a
// non-empty issuer to build the otpauth URI).
const defaultTOTPIssuer = "Proxcloud"

// EnrollTOTP handles POST /api/auth/totp/enroll (Authenticated). It generates a
// fresh RFC-6238 secret, seals it (AES-256-GCM), and stores it UNCONFIRMED —
// enabling nothing until VerifyEnrollTOTP proves possession. 409 if TOTP is
// already enabled (disable first). The response carries the otpauth URI, a
// server-rendered QR, and the manual base32 key; the plaintext secret otherwise
// never leaves the process.
func (h *Handler) EnrollTOTP(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeErr(w, notSignedIn())
		return
	}
	ctx := r.Context()
	user, err := h.Store.GetUserByID(ctx, id.UserID)
	if err != nil {
		h.logger().Error("totp enroll: get user", "err", err)
		writeErr(w, internalErr())
		return
	}
	if user.TOTPEnabled {
		writeErr(w, &types.APIError{Code: "conflict", Message: "Two-step verification is already on. Turn it off first to re-enroll.", Status: http.StatusConflict})
		return
	}

	issuer := h.TOTPIssuer
	if issuer == "" {
		issuer = defaultTOTPIssuer
	}
	key, err := totp.Generate(totp.GenerateOpts{Issuer: issuer, AccountName: user.Email})
	if err != nil {
		h.logger().Error("totp enroll: generate", "err", err)
		writeErr(w, internalErr())
		return
	}
	if h.Secrets == nil {
		h.logger().Error("totp enroll: secrets cipher not configured")
		writeErr(w, internalErr())
		return
	}
	sealed := h.Secrets.Seal([]byte(key.Secret()))
	if err := h.Store.UpsertTOTPSecret(ctx, user.ID, sealed); err != nil {
		h.logger().Error("totp enroll: upsert secret", "err", err)
		writeErr(w, internalErr())
		return
	}

	img, err := key.Image(200, 200)
	if err != nil {
		h.logger().Error("totp enroll: render qr", "err", err)
		writeErr(w, internalErr())
		return
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		h.logger().Error("totp enroll: encode qr", "err", err)
		writeErr(w, internalErr())
		return
	}
	writeJSON(w, http.StatusOK, types.EnrollTOTPResponse{
		OtpauthURI:   key.String(),
		QrPngDataUri: "data:image/png;base64," + base64.StdEncoding.EncodeToString(buf.Bytes()),
		ManualKey:    key.Secret(),
	})
}

// VerifyEnrollTOTP handles POST /api/auth/totp/verify (Authenticated). It loads
// and opens the pending secret, validates the submitted code (±1 step), and — on
// success, in ONE WithTx — confirms the secret, flips users.totp_enabled, and
// replaces the recovery codes with 10 fresh ones (returned ONCE). 409 if there is
// no pending secret or it is already confirmed; 401 on a bad code. Rate-limited
// per-IP; audited totp.enable (fail-closed intent → finalize).
func (h *Handler) VerifyEnrollTOTP(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeErr(w, notSignedIn())
		return
	}
	var req types.VerifyEnrollRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, &types.APIError{Code: "invalid_request", Message: "Request body must be JSON with a code.", Status: http.StatusBadRequest})
		return
	}
	ip := clientIP(r)
	if h.Limiter != nil && !h.Limiter.Allow(ip) {
		h.logger().Warn("totp verify rate limited", "ip", ip)
		writeErr(w, rateLimited())
		return
	}

	ctx := r.Context()
	sec, err := h.Store.GetTOTPSecret(ctx, id.UserID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, &types.APIError{Code: "conflict", Message: "No pending enrollment — start setup first.", Status: http.StatusConflict})
		return
	}
	if err != nil {
		h.logger().Error("totp verify: get secret", "err", err)
		writeErr(w, internalErr())
		return
	}
	if sec.ConfirmedAt != nil {
		writeErr(w, &types.APIError{Code: "conflict", Message: "Two-step verification is already on.", Status: http.StatusConflict})
		return
	}
	if h.Secrets == nil {
		h.logger().Error("totp verify: secrets cipher not configured")
		writeErr(w, internalErr())
		return
	}
	plain, err := h.Secrets.Open(sec.SecretEncrypted)
	if err != nil {
		h.logger().Error("totp verify: open secret", "err", err)
		writeErr(w, internalErr())
		return
	}
	if !totp.Validate(strings.TrimSpace(req.Code), string(plain)) {
		writeErr(w, &types.APIError{Code: "unauthenticated", Message: "That code is not valid — check your authenticator and try again.", Status: http.StatusUnauthorized})
		return
	}

	// Fail-closed audit intent BEFORE the mutation (ADR-0013 §5).
	pending, aerr := h.recorder().Begin(ctx, auditz.Intent{
		Action:      "totp.enable",
		ActorUserID: id.UserID,
		TenantID:    id.ActiveTenantID,
		TargetType:  "user",
		TargetID:    id.UserID,
		IP:          ipPtr(r),
	})
	if aerr != nil {
		h.logger().Error("audit intent for totp.enable failed — enrollment refused", "err", aerr)
		writeErr(w, internalErr())
		return
	}

	codes, err := genRecoveryCodes(recoveryCodeCount)
	if err != nil {
		h.logger().Error("totp verify: gen recovery codes", "err", err)
		pending.Finalize(ctx, "error", map[string]any{"status": http.StatusInternalServerError})
		writeErr(w, internalErr())
		return
	}
	hashes := hashRecoveryCodes(codes)
	err = h.Store.WithTx(ctx, func(tx store.Store) error {
		if e := tx.ConfirmTOTPSecret(ctx, id.UserID); e != nil {
			return e
		}
		if e := tx.SetTOTPEnabled(ctx, id.UserID, true); e != nil {
			return e
		}
		return tx.ReplaceRecoveryCodes(ctx, id.UserID, hashes)
	})
	if err != nil {
		h.logger().Error("totp verify: enable tx", "err", err)
		pending.Finalize(ctx, "error", map[string]any{"status": http.StatusInternalServerError})
		writeErr(w, internalErr())
		return
	}
	if h.Limiter != nil {
		h.Limiter.Reset(ip)
	}
	pending.Finalize(ctx, "success", map[string]any{"status": http.StatusOK})
	h.logger().Info("totp enabled", "user_id", id.UserID)
	writeJSON(w, http.StatusOK, types.VerifyEnrollResponse{RecoveryCodes: codes})
}

// DisableTOTP handles POST /api/auth/totp/disable (Authenticated). It re-verifies
// the caller's password (bcrypt semaphore) and, on success, in ONE WithTx deletes
// the secret + recovery codes and clears users.totp_enabled. Wrong password →
// 401. Audited totp.disable.
func (h *Handler) DisableTOTP(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeErr(w, notSignedIn())
		return
	}
	var req types.PasswordConfirmRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, &types.APIError{Code: "invalid_request", Message: "Request body must be JSON with a password.", Status: http.StatusBadRequest})
		return
	}
	ctx := r.Context()
	if !h.reverifyPassword(ctx, id.UserID, req.Password) {
		writeErr(w, &types.APIError{Code: "unauthenticated", Message: "Password is incorrect.", Status: http.StatusUnauthorized})
		return
	}

	pending, aerr := h.recorder().Begin(ctx, auditz.Intent{
		Action:      "totp.disable",
		ActorUserID: id.UserID,
		TenantID:    id.ActiveTenantID,
		TargetType:  "user",
		TargetID:    id.UserID,
		IP:          ipPtr(r),
	})
	if aerr != nil {
		h.logger().Error("audit intent for totp.disable failed — disable refused", "err", aerr)
		writeErr(w, internalErr())
		return
	}
	err := h.Store.WithTx(ctx, func(tx store.Store) error {
		if e := tx.DeleteTOTPSecret(ctx, id.UserID); e != nil {
			return e
		}
		if e := tx.DeleteRecoveryCodes(ctx, id.UserID); e != nil {
			return e
		}
		return tx.SetTOTPEnabled(ctx, id.UserID, false)
	})
	if err != nil {
		h.logger().Error("totp disable: tx", "err", err)
		pending.Finalize(ctx, "error", map[string]any{"status": http.StatusInternalServerError})
		writeErr(w, internalErr())
		return
	}
	pending.Finalize(ctx, "success", map[string]any{"status": http.StatusNoContent})
	h.logger().Info("totp disabled", "user_id", id.UserID)
	w.WriteHeader(http.StatusNoContent)
}

// RegenerateRecoveryCodes handles POST /api/auth/totp/recovery-codes
// (Authenticated). It re-verifies the caller's password, requires TOTP enabled
// (409 otherwise), and replaces all recovery codes with 10 fresh ones (returned
// ONCE). Audited recovery.regenerate.
func (h *Handler) RegenerateRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeErr(w, notSignedIn())
		return
	}
	var req types.PasswordConfirmRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, &types.APIError{Code: "invalid_request", Message: "Request body must be JSON with a password.", Status: http.StatusBadRequest})
		return
	}
	ctx := r.Context()
	user, err := h.Store.GetUserByID(ctx, id.UserID)
	if err != nil {
		h.logger().Error("recovery regenerate: get user", "err", err)
		writeErr(w, internalErr())
		return
	}
	if !h.verifyPasswordFor(ctx, user, req.Password) {
		writeErr(w, &types.APIError{Code: "unauthenticated", Message: "Password is incorrect.", Status: http.StatusUnauthorized})
		return
	}
	if !user.TOTPEnabled {
		writeErr(w, &types.APIError{Code: "conflict", Message: "Turn on two-step verification before generating recovery codes.", Status: http.StatusConflict})
		return
	}

	pending, aerr := h.recorder().Begin(ctx, auditz.Intent{
		Action:      "recovery.regenerate",
		ActorUserID: id.UserID,
		TenantID:    id.ActiveTenantID,
		TargetType:  "user",
		TargetID:    id.UserID,
		IP:          ipPtr(r),
	})
	if aerr != nil {
		h.logger().Error("audit intent for recovery.regenerate failed — regenerate refused", "err", aerr)
		writeErr(w, internalErr())
		return
	}
	codes, err := genRecoveryCodes(recoveryCodeCount)
	if err != nil {
		h.logger().Error("recovery regenerate: gen codes", "err", err)
		pending.Finalize(ctx, "error", map[string]any{"status": http.StatusInternalServerError})
		writeErr(w, internalErr())
		return
	}
	hashes := hashRecoveryCodes(codes)
	if err := h.Store.WithTx(ctx, func(tx store.Store) error {
		return tx.ReplaceRecoveryCodes(ctx, id.UserID, hashes)
	}); err != nil {
		h.logger().Error("recovery regenerate: replace", "err", err)
		pending.Finalize(ctx, "error", map[string]any{"status": http.StatusInternalServerError})
		writeErr(w, internalErr())
		return
	}
	pending.Finalize(ctx, "success", map[string]any{"status": http.StatusOK})
	h.logger().Info("recovery codes regenerated", "user_id", id.UserID)
	writeJSON(w, http.StatusOK, types.RecoveryCodesResponse{RecoveryCodes: codes})
}

// LoginTOTP handles POST /api/auth/login/totp (Public). It reads the interim
// proxcloud_totp challenge, rejects a missing/expired/consumed one with 401
// totp_challenge_expired, then validates a 6-digit TOTP OR a recovery code
// (auto-detected by shape). On success it consumes the challenge, issues the real
// (rotated) session bound to a sensible active tenant, clears the challenge
// cookie, and returns 204. On failure it increments the challenge's attempt
// counter; the 5th failure self-consumes the challenge → totp_challenge_expired,
// otherwise 401 unauthenticated. Per-IP rate-limited; audited totp.login.
func (h *Handler) LoginTOTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, cerr := r.Cookie(ChallengeCookieName)
	if cerr != nil || c.Value == "" {
		writeErr(w, challengeExpired())
		return
	}
	ch, err := h.Store.GetLoginChallengeByTokenHash(ctx, hashToken(c.Value))
	if err != nil || ch.ConsumedAt != nil || h.Sessions.now().After(ch.ExpiresAt) {
		http.SetCookie(w, h.Sessions.ClearChallengeCookie())
		writeErr(w, challengeExpired())
		return
	}
	ip := clientIP(r)
	if h.Limiter != nil && !h.Limiter.Allow(ip) {
		h.logger().Warn("login/totp rate limited", "ip", ip)
		writeErr(w, rateLimited())
		return
	}
	var req types.LoginTOTPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, &types.APIError{Code: "invalid_request", Message: "Request body must be JSON with a code.", Status: http.StatusBadRequest})
		return
	}
	code := strings.TrimSpace(req.Code)

	// Fail-closed audit intent BEFORE we act on the challenge.
	pending, aerr := h.recorder().Begin(ctx, auditz.Intent{
		Action:      "totp.login",
		ActorUserID: ch.UserID,
		TargetType:  "user",
		TargetID:    ch.UserID,
		IP:          ipPtr(r),
	})
	if aerr != nil {
		h.logger().Error("audit intent for totp.login failed — login refused", "err", aerr)
		writeErr(w, internalErr())
		return
	}

	// Accepted trade-off (security LOW, availability-only): secondFactorValid
	// consumes a recovery code atomically BEFORE ConsumeLoginChallenge below. So if
	// the challenge is lost in a race between these two steps, a valid recovery code
	// is spent without a session being issued — the user must use another code. It
	// is not exploitable (no auth is granted; the code was single-use anyway). We do
	// not reorder here: consuming the challenge first would complicate the
	// RecordChallengeFailure-on-bad-code path (we would have to distinguish a bad
	// code from a lost challenge after the challenge is already gone), so the
	// security-critical ordering is left as-is in this pass.
	ok, verr := h.secondFactorValid(ctx, ch.UserID, code)
	if verr != nil {
		h.logger().Error("login/totp: verify second factor", "err", verr)
		pending.Finalize(ctx, "error", map[string]any{"status": http.StatusInternalServerError})
		writeErr(w, internalErr())
		return
	}
	if !ok {
		locked, rerr := h.Store.RecordChallengeFailure(ctx, ch.ID, maxTOTPAttempts)
		if rerr != nil {
			h.logger().Error("login/totp: record failure", "err", rerr)
			pending.Finalize(ctx, "error", map[string]any{"status": http.StatusInternalServerError})
			writeErr(w, internalErr())
			return
		}
		if locked {
			http.SetCookie(w, h.Sessions.ClearChallengeCookie())
			pending.Finalize(ctx, "denied", map[string]any{"status": http.StatusUnauthorized, "locked": true})
			writeErr(w, challengeExpired())
			return
		}
		pending.Finalize(ctx, "denied", map[string]any{"status": http.StatusUnauthorized})
		writeErr(w, unauthenticated())
		return
	}

	// Second factor valid: consume the challenge (single-use), then issue the
	// real session. If the consume lost a race the challenge is already spent.
	won, cerr := h.Store.ConsumeLoginChallenge(ctx, ch.ID)
	if cerr != nil {
		h.logger().Error("login/totp: consume challenge", "err", cerr)
		pending.Finalize(ctx, "error", map[string]any{"status": http.StatusInternalServerError})
		writeErr(w, internalErr())
		return
	}
	if !won {
		http.SetCookie(w, h.Sessions.ClearChallengeCookie())
		pending.Finalize(ctx, "denied", map[string]any{"status": http.StatusUnauthorized})
		writeErr(w, challengeExpired())
		return
	}

	// Retire any prior session this browser still carried for the same user, and
	// pick a sensible active tenant (a prior session's, or the user's sole tenant).
	var priorSessionID string
	activeTenant := ""
	if prev, perr := h.Sessions.Verify(ctx, r); perr == nil && prev.UserID == ch.UserID {
		priorSessionID = prev.SessionID
		activeTenant = prev.ActiveTenantID
	}
	if activeTenant == "" {
		activeTenant = h.soleTenant(ctx, ch.UserID)
	}

	cookie, ierr := h.issueSessionForTenant(ctx, ch.UserID, activeTenant, r)
	if ierr != nil {
		h.logger().Error("login/totp: issue session", "err", ierr)
		pending.Finalize(ctx, "error", map[string]any{"status": http.StatusInternalServerError})
		writeErr(w, internalErr())
		return
	}
	if priorSessionID != "" {
		if rerr := h.Sessions.Revoke(ctx, priorSessionID); rerr != nil {
			h.logger().Warn("login/totp: revoke prior session", "err", rerr) // non-fatal
		}
	}
	if h.Limiter != nil {
		h.Limiter.Reset(ip)
	}
	http.SetCookie(w, cookie)
	http.SetCookie(w, h.Sessions.ClearChallengeCookie())
	pending.Finalize(ctx, "success", map[string]any{"status": http.StatusNoContent, "user_id": ch.UserID})
	h.logger().Info("login/totp ok", "user_id", ch.UserID)
	w.WriteHeader(http.StatusNoContent)
}

// secondFactorValid reports whether code satisfies the user's second factor: a
// 6-digit string is checked as a TOTP against the confirmed secret; anything else
// is treated as a recovery code (SHA-256 normalized, consumed atomically). A
// missing/unopenable secret makes a TOTP attempt simply invalid (not an error);
// only unexpected store failures return a non-nil error.
func (h *Handler) secondFactorValid(ctx context.Context, userID, code string) (bool, error) {
	if isSixDigits(code) {
		sec, err := h.Store.GetTOTPSecret(ctx, userID)
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if sec.ConfirmedAt == nil || h.Secrets == nil {
			return false, nil
		}
		plain, err := h.Secrets.Open(sec.SecretEncrypted)
		if err != nil {
			return false, err
		}
		return totp.Validate(code, string(plain)), nil
	}
	return h.Store.ConsumeRecoveryCode(ctx, userID, hashRecoveryCode(code))
}

// soleTenant returns the user's tenant id when they can reach exactly one, else
// "" (the frontend then prompts for a tenant, as it does after a normal login).
func (h *Handler) soleTenant(ctx context.Context, userID string) string {
	tws, err := h.Store.ListTenantsForUser(ctx, userID)
	if err != nil || len(tws) != 1 {
		return ""
	}
	return tws[0].ID
}

// reverifyPassword loads the user and checks the password behind the bcrypt
// semaphore. A missing user or missing hash fails closed.
func (h *Handler) reverifyPassword(ctx context.Context, userID, password string) bool {
	user, err := h.Store.GetUserByID(ctx, userID)
	if err != nil {
		return false
	}
	return h.verifyPasswordFor(ctx, user, password)
}

// verifyPasswordFor checks password against an already-loaded user, bounding the
// hash comparison with the shared bcrypt semaphore. A credential-less user still
// spends hashing time (VerifyDummy) so timing does not distinguish it.
func (h *Handler) verifyPasswordFor(_ context.Context, user *store.User, password string) bool {
	if h.Limiter != nil {
		release := h.Limiter.AcquireBcrypt()
		defer release()
	}
	if user.PasswordHash == nil {
		h.Hasher.VerifyDummy(password)
		return false
	}
	ok, _ := h.Hasher.Verify(password, *user.PasswordHash, algoOf(user))
	return ok
}

// --- recovery codes (ADR-0013 §4) ---

// genRecoveryCodes returns n high-entropy "XXXXX-XXXXX" Crockford-base32 codes
// from crypto/rand (~50 bits each). They are shown to the user exactly once.
func genRecoveryCodes(n int) ([]string, error) {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		raw := make([]byte, 2*recoveryCodeHalf)
		if _, err := rand.Read(raw); err != nil {
			return nil, err
		}
		var b strings.Builder
		b.Grow(2*recoveryCodeHalf + 1)
		for j, v := range raw {
			if j == recoveryCodeHalf {
				b.WriteByte('-')
			}
			b.WriteByte(crockford[v&0x1F])
		}
		out = append(out, b.String())
	}
	return out, nil
}

// hashRecoveryCodes maps a slice of plaintext codes to their storage hashes.
func hashRecoveryCodes(codes []string) []string {
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = hashRecoveryCode(c)
	}
	return hashes
}

// hashRecoveryCode is the storage/lookup hash of a recovery code: SHA-256 over
// the normalized code (unsalted, mirroring session tokens — ADR-0013 §4).
func hashRecoveryCode(code string) string {
	sum := sha256.Sum256([]byte(normalizeRecoveryCode(code)))
	return hex.EncodeToString(sum[:])
}

// normalizeRecoveryCode canonicalizes user input before hashing: strip hyphens
// and whitespace, uppercase, and fold the Crockford-ambiguous glyphs (I, L → 1;
// O → 0) so a code typed with look-alikes still matches.
func normalizeRecoveryCode(code string) string {
	var b strings.Builder
	b.Grow(len(code))
	for _, r := range strings.ToUpper(code) {
		switch r {
		case '-', ' ', '\t', '\n', '\r':
			continue
		case 'I', 'L':
			b.WriteByte('1')
		case 'O':
			b.WriteByte('0')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// isSixDigits reports whether s is exactly six ASCII digits (a TOTP shape).
func isSixDigits(s string) bool {
	if len(s) != 6 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// mintChallengeToken returns a fresh 256-bit interim login-challenge token
// (base64url), carried in the proxcloud_totp cookie; only its hash is stored.
func mintChallengeToken() (string, error) {
	raw := make([]byte, challengeTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// challengeExpired is the single 401 for any missing/expired/consumed/locked
// interim challenge — the frontend restarts sign-in on it.
func challengeExpired() *types.APIError {
	return &types.APIError{Code: "totp_challenge_expired", Message: "Your verification session expired. Sign in again.", Status: http.StatusUnauthorized}
}

// notSignedIn is the 401 for an authenticated-surface handler reached without an
// Identity (defense in depth — Authenticate normally gates these).
func notSignedIn() *types.APIError {
	return &types.APIError{Code: "unauthenticated", Message: "Not signed in.", Status: http.StatusUnauthorized}
}
