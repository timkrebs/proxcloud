# Phase 5 — Invitations + TOTP: build spec

Branch: `feat/multi-tenancy`. Final multi-tenancy phase. Builds on Phases 1–4
(committed, green). Implements ADR-0013 (invitations, TOTP, secrets-at-rest).
Wire contract: `docs/api/phase5-invitations-totp.md` — do not deviate from the
shapes there. Execution spec for **backend-engineer** and **frontend-engineer**.

Fills existing seams: the auth `Handler` (`internal/auth/handlers.go`), the
`Store` sub-interface pattern, the `auditz.Recorder` (ADR-0012), the
`LoginLimiter`, `config.SecretsKey`, `AuditOnMutation` + the two completeness
tests, and the SignInCard / Settings / members screens. One new migration
(`000004`), one new lib (`pquerna/otp`), two new internal packages
(`internal/secrets`, `internal/mail`).

---

## 0. Decisions that need Tim (each already chosen so nothing is blocked)

1. **Email driver default = log-to-console.** `Mailer` defaults to a dev mailer
   that prints the full message (incl. accept link) to **stdout** (not the slog
   logger, so the raw token is never in structured/access logs). `SMTPMailer`
   activates when `SMTP_HOST` is set. **Confirm** (vs. requiring SMTP always).
2. **Recovery codes: 10 codes, `XXXXX-XXXXX` Crockford base32 (~50 bits), stored
   unsalted SHA-256, single-use.** O(1) consume-by-hash, mirrors session tokens.
   **Confirm** count/format (vs. 8 codes, or Argon2id/salted).
3. **Login interim challenge TTL = 5m, single-use, 5-attempt lockout**, carried in
   a separate `proxcloud_totp` HttpOnly cookie backed by a stored+hashed
   `login_challenges` row. **Confirm** the 5m / 5-attempt numbers.
4. **Audit `password.change` now.** It is unaudited since Phase 2 (flagged in the
   Phase-4 review); Phase 5 adds an `auditz` `password.change` row. **Confirm.**
5. **Invite-accept treats holding the token as proof of mailbox control** — no
   separate email-verification step; the accepted user’s email is bound to the
   invite (immutable). A signed-in user whose email ≠ invite email is refused
   (`409 email_mismatch`). **Confirm** (vs. adding an `email_verified` column).
6. **Migration 000004 adds only `login_challenges`** — `invitations`,
   `totp_secrets`, `recovery_codes` already exist (000001), used as-is. Stated.
7. **`POST /api/auth/login` return changes 204 → 200 `LoginResponse`.** Minor
   contract change so the client learns `totpRequired`. Stated, not blocking.

---

## 1. Secrets helper, migration & store methods (backend-engineer)

### 1.1 `internal/secrets` — AES-256-GCM (new package)
```go
type Cipher struct { aead cipher.AEAD }
func New(key []byte) (*Cipher, error)          // len(key)==32 else error
func (c *Cipher) Seal(plaintext []byte) []byte // nonce(12B) ‖ gcm.Seal; random nonce/call
func (c *Cipher) Open(blob []byte) ([]byte, error) // splits nonce; AEAD verifies (tamper → error)
```
Constructed in `main.go` from `cfg.SecretsKey`; injected into `auth.Handler`
(new field `Secrets *secrets.Cipher`). Unit test: Seal→Open round-trips; a
flipped byte → `Open` error; two Seals of the same input differ (nonce).

### 1.2 Migration `000004_login_challenges` (backend-engineer)
```sql
-- up
CREATE TABLE login_challenges (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_hash  text NOT NULL UNIQUE,
    user_id     uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    attempts    integer NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now(),
    expires_at  timestamptz NOT NULL,
    consumed_at timestamptz,
    ip          text,
    user_agent  text
);
CREATE INDEX login_challenges_user_id_idx ON login_challenges (user_id);
-- down: DROP TABLE login_challenges;
```
Do **not** recreate `invitations`/`totp_secrets`/`recovery_codes`.

### 1.3 New store sub-interfaces (`store.go` + `postgres.go` + `storetest/fake.go`)
Add to the `Store` interface (mirror existing sub-interface style). All lookups by
hash mirror `GetSessionByTokenHash`; all “consume” ops are single-statement
`UPDATE … WHERE … IS NULL` returning rows-affected so single-use is atomic.

```go
type InvitationStore interface {
    CreateInvitation(ctx, CreateInvitationParams) (*Invitation, error) // deletes any pending dup for (email,scope) first, in caller tx
    GetInvitationByTokenHash(ctx, tokenHash string) (*Invitation, error) // ErrNotFound
    ListPendingInvitationsByScopes(ctx, scopeType string, scopeIDs []string) ([]Invitation, error) // accepted_at IS NULL
    MarkInvitationAccepted(ctx, id string) (bool, error) // UPDATE … WHERE accepted_at IS NULL; false ⇒ raced
    DeleteInvitation(ctx, id string) error               // revoke; ErrNotFound if gone
}
type TOTPStore interface {
    UpsertTOTPSecret(ctx, userID string, secretEncrypted []byte) error   // ON CONFLICT(user_id) reset confirmed_at=NULL
    GetTOTPSecret(ctx, userID string) (*TOTPSecret, error)               // ErrNotFound
    ConfirmTOTPSecret(ctx, userID string) error                          // SET confirmed_at WHERE confirmed_at IS NULL
    DeleteTOTPSecret(ctx, userID string) error
}
type RecoveryCodeStore interface {
    ReplaceRecoveryCodes(ctx, userID string, codeHashes []string) error  // DELETE all + INSERT set (caller tx)
    ConsumeRecoveryCode(ctx, userID, codeHash string) (bool, error)      // UPDATE … WHERE used_at IS NULL
    CountUnusedRecoveryCodes(ctx, userID string) (int, error)
    DeleteRecoveryCodes(ctx, userID string) error
}
type LoginChallengeStore interface {
    CreateLoginChallenge(ctx, CreateLoginChallengeParams) (*LoginChallenge, error)
    GetLoginChallengeByTokenHash(ctx, tokenHash string) (*LoginChallenge, error) // ErrNotFound
    ConsumeLoginChallenge(ctx, id string) (bool, error)                  // success path (single-use)
    RecordChallengeFailure(ctx, id string, maxAttempts int) (locked bool, err error) // ++attempts; consume if ≥max
}
```
Add domain structs `TOTPSecret{UserID; SecretEncrypted []byte; ConfirmedAt *time.Time}`
and `LoginChallenge{ID; UserID; Attempts int; ExpiresAt; ConsumedAt *time.Time; …}`.
Unique-violation on `CreateInvitation` maps to `ErrConflict` (reuse the existing
mapper). Extend `storetest.Fake` with in-memory equivalents (single-use semantics
enforced in-memory so handler tests pass without Postgres).

---

## 2. Invitations (backend-engineer)

### 2.1 Management — `internal/handlers/invitations.go` (tenant-scoped, Owner)
Mount in `MountTenant`: `POST/GET p+"/invitations"`, `DELETE
p+"/invitations/{invitationId}"`. These flow through `AuditOnMutation` (POST/DELETE)
— add registry + action-map entries (§ contract). Handlers:
- **CreateInvitation**: validate email/role; if `scopeType=="project"` load the
  project and 404 unless `TenantID==ActiveTenantID`; **cap the granted role at the
  inviter’s `EffectiveRole`** (`RoleAtLeast` from authz). Mint token
  (`crypto/rand` 32B, base64url), store `hashToken(token)` + row; call
  `d.Mailer.Send` with the accept link. Return **201** `Invitation` (no token).
  `audit.Annotate(ctx,"email"/"role"/"scope")` for detail.
- **ListInvitations**: `ListPendingInvitationsByScopes("tenant",[tenantId])` +
  the tenant’s project ids; resolve scope labels from the projects map + tenant;
  compute `Status`.
- **RevokeInvitation**: load by id, 404 unless it belongs to `{tenantId}`,
  `DeleteInvitation`. **204**.

### 2.2 Accept — extend `internal/auth/handlers.go` (public)
New `ValidateInvite` (GET) + `AcceptInvite` (POST), wired as **public** routes in
`router.go` (next to `/auth/login`). `ValidateInvite`: hash → `GetInvitationBy…`
→ generic **404** on miss/expired/accepted; else build `InvitationDetails`
(`RequiresAccount` from `GetUserByEmail`; `SignedInMatches` from `Sessions.Verify`).
`AcceptInvite`: per-IP `Limiter.Allow` + `AcquireBcrypt`; the full `WithTx` flow in
the contract §accept (create-or-attach, membership from row, `MarkInvitationAccepted`
guard, issue rotated session + set active tenant). Audited via `auditz` action
`invitation.accept`. New `auth.Handler` fields: `Store` (exists), `Secrets`,
`Mailer`, `InvitationTTL`, plus an `auditz.Recorder` built from `Store`.

---

## 3. TOTP + recovery codes — extend `internal/auth` (backend-engineer)

New file `internal/auth/totp.go`. Add `pquerna/otp` to `go.mod`. RFC-6238
defaults (SHA1/6/30, ±1 skew). Handlers (authenticated account surface, wired in
`router.go` inside the post-`Authenticate` group; registry = Authenticated):
- **EnrollTOTP**: 409 if `user.TOTPEnabled`; `totp.Generate({Issuer:cfg.TOTPIssuer,
  AccountName:email})`; `Secrets.Seal([]byte(key.Secret()))` → `UpsertTOTPSecret`;
  render QR via `key.Image(200,200)`→PNG→base64 data URI; return `otpauthUri`,
  `qrPngDataUri`, `manualKey`.
- **VerifyEnrollTOTP**: `GetTOTPSecret` (409 if none/already confirmed);
  `Secrets.Open`; `totp.Validate(code, secret)`; on ok `WithTx{ ConfirmTOTPSecret,
  SetTOTPEnabled(true), ReplaceRecoveryCodes(gen 10) }`; return the plaintext
  codes once. Rate-limited. Audit `totp.enable`.
- **DisableTOTP**: decode `{password}`; re-verify (bcrypt semaphore); `WithTx{
  DeleteTOTPSecret, DeleteRecoveryCodes, SetTOTPEnabled(false) }`. Audit
  `totp.disable`.
- **RegenerateRecoveryCodes**: `{password}` re-verify; 409 unless enabled;
  `ReplaceRecoveryCodes(gen 10)`; return once. Audit `recovery.regenerate`.

Recovery-code helper (new): `genRecoveryCodes(n)` → `[]string` (`XXXXX-XXXXX`
Crockford base32 from `crypto/rand`); `hashRecoveryCode(code)` =
`sha256(normalize(code))` where normalize upstrips hyphens/spaces + uppercases +
folds Crockford ambiguities (I/L→1, O→0). `Me` handler adds
`recoveryCodesRemaining` from `CountUnusedRecoveryCodes`.

---

## 4. Login second factor + rate limits (backend-engineer)

### 4.1 Modify `Login`, add `LoginTOTP` (`internal/auth/handlers.go`)
- `Login`: after the existing password-success block, branch on
  `user.TOTPEnabled`. **Disabled** → issue session (as today) but respond **200**
  `LoginResponse{false}`. **Enabled** → do NOT issue a session; mint challenge
  token, `CreateLoginChallenge{UserID, hash, now+LoginChallengeTTL, ip, ua}`, set
  `proxcloud_totp` cookie (see 4.3), respond **200** `LoginResponse{true}`.
- `LoginTOTP` (new, public): read `proxcloud_totp` cookie → challenge by hash;
  reject missing/expired/consumed with **401** `totp_challenge_expired`. Detect
  code shape: 6 digits → TOTP (`Open` + `totp.Validate`); else → recovery
  (`ConsumeRecoveryCode`). Success: `ConsumeLoginChallenge`, issue rotated
  session, `SetSessionActiveTenant` (default tenant or last), clear the totp
  cookie, **204**. Failure: `RecordChallengeFailure(id,maxTOTPAttempts=5)`; if
  locked → `totp_challenge_expired`, else **401** `unauthenticated`. Audit
  `totp.login` (auditz; success/denied).

### 4.2 Rate limits (`internal/auth/ratelimit.go`)
Reuse `LoginLimiter.Allow` (per-IP window) on **login, login/totp, invitations
validate, invitations accept, totp verify-enroll** (mirror `Login`/`Bootstrap`).
Per-**account** lockout for the second factor is the challenge `attempts` counter
(DB-backed, multi-instance-safe) — no new in-memory structure. `AcquireBcrypt`
guards the Argon2id/bcrypt paths on **accept** (new-user hash) and **disable/
regenerate** (password re-verify). No semaphore on TOTP verify (cheap HMAC).

### 4.3 Cookie helper (`internal/auth/session.go`)
Add `IssueChallengeCookie(token)` / `ClearChallengeCookie()` returning a cookie
named `proxcloud_totp`, `Path:/api/auth`, HttpOnly, `Secure=s.secure`,
SameSite=Lax, `MaxAge=int(LoginChallengeTTL.Seconds())`. `Authenticate`/`Verify`
must **never** read this cookie — assert this in a test.

---

## 5. Mailer + config (backend-engineer)

### 5.1 `internal/mail` (new package)
```go
type Message struct { To, Subject, TextBody, HTMLBody string }
type Mailer  interface { Send(ctx context.Context, m Message) error }
type LogMailer struct { W io.Writer } // default os.Stdout; prints full message incl. link
type SMTPMailer struct { Host,Port,User,Pass,From string; StartTLS bool } // net/smtp
```
`LogMailer.Send` writes a clearly-marked `--- DEV MAILER ---` block (incl. the
accept URL) to `W`; it MUST NOT use the slog logger (keeps the raw token out of
structured/access logs — ADR-0013). Invite email builder lives here (subject +
text/HTML with the accept link). Select in `main.go`: `SMTP_HOST` set →
`SMTPMailer`, else `LogMailer`.

### 5.2 `internal/config/config.go` additions (validated at boot)
- `SMTP_HOST`, `SMTP_PORT` (default 587), `SMTP_USERNAME`, `SMTP_PASSWORD`,
  `SMTP_FROM`, `SMTP_STARTTLS` (default true). If `SMTP_HOST` set, `SMTP_FROM`
  required (else problem). Never logged.
- `INVITATION_TTL` (duration, default `72h`), `LOGIN_CHALLENGE_TTL` (default `5m`),
  `TOTP_ISSUER` (default `Proxcloud`). `FRONTEND_ORIGIN` (exists) is the accept-
  link base — add a problem if invitations are usable but it is empty (warn: links
  will be relative/unusable). Update `.env.example` + `docs/deployment.md`.

---

## 6. Audit (backend-engineer)

- Tenant-scoped `invitation.create` / `invitation.revoke` → `AuditOnMutation`
  (registry + action-map entries; the two completeness tests enforce presence).
- Account-level `invitation.accept`, `totp.enable`, `totp.disable`, `totp.login`,
  `recovery.regenerate`, and **`password.change`** → `auditz.Recorder`
  (`Begin` intent before the mutation, `Finalize` outcome after), exactly like
  `admin.CreateTenantAdmin` / `quotas.go`. Add the recorder to `auth.Handler`
  and call it in each handler. Decision F4: `ChangePassword` gains a
  `password.change` audit row (tenant = the user’s active tenant, actor = self).

---

## 7. Frontend (frontend-engineer)

Regenerate `lib/api/generated/types.ts` (tygo) after the Go types land. New
data-layer file `lib/api/security.ts` with hooks: `useValidateInvite(token)`,
`useAcceptInvite`, `useEnrollTotp`, `useVerifyEnrollTotp`, `useDisableTotp`,
`useRegenerateRecoveryCodes`, `useInvitations()` (Owner list),
`useCreateInvitation`, `useRevokeInvitation`. Tenant-scoped invite hooks are
prefixed `/api/tenants/${tenantId}/…` and invalidate `qk.members` +
`qk.invitations(tenantId)`.

- **SignInCard (`components/auth/SignInCard.tsx`)** — add a third `step: "totp"`.
  `handleSubmit` now reads `LoginResponse`; `totpRequired` → advance to the TOTP
  step (a 6-digit input + a “Use a recovery code” toggle) that posts to
  `/api/auth/login/totp`; `totp_challenge_expired` → toast + back to `email`
  step. Non-TOTP login → dashboard as before.
- **Accept screen (`app/(auth)/invite/[token]/page.tsx`, new)** — fetch
  `InvitationDetails`; render tenant/role/scope. Branch: `requiresAccount` → name
  + password form (reuse `PASSWORD_RULE` validation); `signedInMatches` → single
  “Accept invitation” button; existing-account-not-signed-in → “Sign in to accept”
  linking to sign-in with a return path. On success → dashboard of the invited
  tenant. Loading / invalid (404) / expired states per DoD.
- **Settings (`app/(portal)/settings/page.tsx`)** — new **Two-step verification**
  section. Disabled: “Set up” → flyout showing `qrPngDataUri` + `manualKey` + a
  6-digit confirm input → on success show the recovery codes **once** (copy/
  download, “I saved these” gate). Enabled: show `recoveryCodesRemaining`,
  “Regenerate recovery codes” (password re-prompt) and “Turn off” (password
  re-prompt). Reuse the `Section`/`FieldRow`/`Card` primitives already in the file.
- **Members screen (tenant admin)** — the current `useMembers` list gains, for
  Owners: an **Invite** button (flyout: email + scope [tenant or a project] + role)
  posting `useCreateInvitation`, and a **Pending invitations** table
  (`useInvitations`) with role/scope/expiry/status and a **Revoke** action. Hide
  all controls for non-Owners. If Phase 3 left members as a hook without a page,
  create `app/(portal)/members/page.tsx` using the projects-page patterns.

All new screens: loading skeleton, empty state, explicit error state; design
tokens only; no fabricated data.

---

## 8. New / changed files index

### New (backend)
`backend/migrations/000004_login_challenges.{up,down}.sql`;
`backend/internal/secrets/{secrets.go,secrets_test.go}`;
`backend/internal/mail/{mail.go,smtp.go,log.go,invite.go}`;
`backend/internal/auth/{totp.go,invitations.go}` (+ `_test.go` each);
`backend/internal/handlers/invitations.go` (+ test);
`backend/api/types/{invitation.go,totp.go}`.
### Changed (backend)
`internal/store/{store.go,postgres.go,storetest/fake.go}` (4 sub-interfaces +
structs); `internal/auth/{handlers.go (Login→LoginResponse, LoginTOTP, accept,
totp handlers, password.change audit), session.go (challenge cookie),
ratelimit.go (reuse), bootstrap.go? no}`; `internal/authz/{permissions.go (+10),
audit_actions.go (+2)}`; `internal/handlers/handlers.go` (MountTenant +3);
`internal/config/config.go` (SMTP/TTLs/issuer); `backend/api/types/auth.go`
(`Me.recoveryCodesRemaining`, `LoginResponse`); `backend/cmd/proxcloud/main.go`
(build `secrets.Cipher`, `Mailer`; inject into `auth.Handler`); `backend/go.mod`
(`pquerna/otp`); `internal/httpserver/router.go` (new public + authenticated auth
routes).
### New/changed (frontend)
new `lib/api/security.ts`, `app/(auth)/invite/[token]/page.tsx`, TOTP-enroll
flyout, invite flyout, members page (if absent); changed `SignInCard.tsx`,
`settings/page.tsx`, `queryKeys.ts` (`invitations`), `lib/api/generated/types.ts`
(regenerated), login mutation/handler for `LoginResponse`.

---

## 9. Sequencing (within Phase 5)

1. **Secrets + migration + stores** — `internal/secrets`; migration 000004; the 4
   store sub-interfaces + structs + fake. `go test ./...` green, no behavior change.
2. **Mailer + config** — `internal/mail`; config additions; main.go wiring
   (Cipher + Mailer into `auth.Handler`).
3. **Invitations** — create/list/revoke (tenant, audited) + validate/accept
   (public, auditz) + registry/action-map + completeness tests green.
4. **TOTP + recovery** — enroll/verify-enroll/disable/regenerate + `Me` count +
   auditz.
5. **Login second factor** — `Login`→`LoginResponse`, `LoginTOTP`, challenge
   cookie, rate limits, `totp.login` audit.
6. **Frontend** — types regen; SignInCard TOTP step; invite accept screen; Settings
   TOTP; members invite + pending list.
7. **Tests + live demo** — invite → accept → TOTP-protected login as the new
   member against `pve01`.

---

## 10. Security acceptance criteria (hand to security-reviewer)

- **Invite tokens unguessable + hash-only:** DB stores only `token_hash`; a table
  test asserts no raw token is ever persisted or returned in any response
  (`Invitation`/`InvitationDetails` carry no token field).
- **Role-in-DB tamper-proof:** accepting a token creates the membership from the
  **row’s** `scope_type/scope_id/role`; a test mutates the client-side URL/body and
  confirms the granted role is unchanged. An Owner cannot mint a grant above their
  own effective role (403/400).
- **Single-use + expiring:** accepting twice → second attempt 404/409 and no
  second membership; an expired invite → 404 on both validate and accept. Revoke
  deletes the row (subsequent validate → 404).
- **Enumeration-safe:** unknown/expired/used token and unknown email all return the
  **same generic 404** on validate; accept never reveals account existence beyond
  the documented `409 account_exists` (which requires holding a valid token).
- **TOTP secret encrypted at rest:** `totp_secrets.secret_encrypted` is AES-256-GCM
  ciphertext (Seal); a test confirms the plaintext base32 never appears in the
  column and that a flipped ciphertext byte fails `Open` (fail-closed).
- **Recovery codes hashed + single-use:** stored as SHA-256 (no plaintext);
  consuming a code marks `used_at` atomically; a reused code → 401; codes are
  returned exactly once (enable/regenerate) and never by `Me`/any GET.
- **Interim challenge is not a session:** a test hits every `Authenticated`/tenant
  route with only the `proxcloud_totp` cookie and gets 401 — `Authenticate` never
  accepts it. The challenge is single-use (second `login/totp` with the same
  cookie → `totp_challenge_expired`), expiring, and locks after 5 failures.
- **Rate limits enforced:** per-IP window on login/login-totp/invite-accept/
  invite-validate/totp-verify (429 past the cap); per-account lockout via the
  challenge counter; bcrypt semaphore bounds Argon2id on accept/disable/regenerate.
- **Every security mutation audited:** one audit row for invite.create/revoke
  (structural, via completeness tests) and for invite.accept, totp.enable/disable/
  login, recovery.regenerate, **password.change** (behavioral auditz tests). A
  forced intent-insert failure refuses the mutation (fail-closed).
- **No cross-tenant leak:** invite list/create/revoke filter by `{tenantId}` in
  SQL; a project `scopeId` from another tenant → 404; revoking another tenant’s
  invite → 404.
</content>
