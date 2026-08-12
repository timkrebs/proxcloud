# ADR-0013: Invitations, TOTP second factor & secrets-at-rest encryption

Date: 2026-08-12 · Status: accepted · Phase 5 (final multi-tenancy phase)

## Context

Phases 1–4 shipped local auth (ADR-0006: DB sessions, Argon2id), tenancy
(ADR-0007), and quotas/audit (ADR-0012). Phase 5 closes the loop: an Owner must
be able to grow their tenant by **inviting** users, and every account must be
protectable with a **TOTP second factor**. The schema already carries the target
tables (`invitations`, `totp_secrets`, `recovery_codes`) from migration 000001,
and `config.SecretsKey` (32 bytes, validated) is loaded but unused. This ADR
records the four non-obvious choices Phase 5 forces: how invite tokens are made
tamper-proof, how a TOTP secret is protected at rest, how the login second factor
is carried without minting a full session, and how recovery codes are stored.

## Decision

### 1. Invitations — role bound in the DB row, token stored only as a hash
An invite is a `crypto/rand` 256-bit token; only its **SHA-256 hash** is stored
(`invitations.token_hash`, unique) — identical to the session-token pattern
(ADR-0006), so a DB leak never yields a usable token. The granted **scope + role
live in the row, never in the token** (`scope_type`, `scope_id`, `role`): the
token is an opaque unguessable lookup key with zero authority of its own, so it
cannot be tampered to escalate. Invites are **single-use** (`accepted_at`) and
**expiring** (`expires_at`, `INVITATION_TTL` default 72h). Accept runs in one
`WithTx`: re-read by hash → assert unexpired + unaccepted → create-or-attach the
user → create the membership **from the row's scope/role** → stamp `accepted_at`
(guarded `WHERE accepted_at IS NULL`, so a double-accept race loses cleanly).

### 2. Secrets at rest — `internal/secrets` AES-256-GCM helper
A new `secrets.Cipher` wraps `SECRETS_KEY`: `Seal(plaintext) → nonce(12B)‖ciphertext`
(random nonce per call, GCM auth tag included), `Open` reverses it. The TOTP
**shared secret is Seal'd** before it touches `totp_secrets.secret_encrypted`
(`bytea`) and only ever decrypted in-process to validate a code. GCM (AEAD) is
chosen over raw CBC/CTR so tampered ciphertext fails closed on `Open`.

### 3. TOTP — RFC-6238 via `pquerna/otp`; interim login carried by a *stored* challenge
`github.com/pquerna/otp` (vetted, standard) generates the secret, the
`otpauth://` URI, and the QR PNG; validation is RFC-6238 default params (SHA1, 6
digits, 30s period, ±1 step skew). Enroll stores the secret **encrypted and
unconfirmed** (`confirmed_at` NULL); a correct code confirms it, flips
`users.totp_enabled`, and issues recovery codes — all in one `WithTx`.

The login second factor must **not** be a full session before the second factor
succeeds. We reject a stateless signed JWT for the interim state for the same
reason ADR-0006 rejected stateless sessions: it cannot be revoked and a Verify
bug could promote it. Instead, password-success-with-TOTP mints a **stored,
hashed, single-use `login_challenge`** (new table, migration 000004) carried in a
**separate** `proxcloud_totp` HttpOnly cookie (TTL `LOGIN_CHALLENGE_TTL`, default
5m). The authenticated middleware only ever accepts `proxcloud_session`, never
the challenge cookie — so the challenge grants **nothing** except the right to
attempt step two for its bound `user_id`. Step two verifies the code (or a
recovery code), consumes the challenge, and only then issues the real session.
The challenge row counts failures; at 5 it self-consumes (forces re-entry of the
password), giving per-account lockout without in-process state (multi-instance
safe, like the advisory-lock reservation).

### 4. Recovery codes — high-entropy, unsalted SHA-256, single-use
Ten codes generated at TOTP-enable (`XXXXX-XXXXX`, Crockford base32, ~50 bits
each), **shown exactly once**, stored as **unsalted SHA-256(normalized code)** and
consumed via `used_at`. Unsalted SHA-256 with O(1) hash-lookup is the deliberate
choice (mirrors session tokens): the codes are high-entropy random values, so a
salt/Argon2id buys nothing an attacker with the DB could not already brute past
faster elsewhere, while O(1) lookup keeps the login path cheap and lets us mark a
single code used atomically.

### 5. Account-security mutations are audited via `auditz` (not the tenant choke-point)
Invite create/revoke are tenant-scoped and already flow through `AuditOnMutation`.
The account-level mutations (`totp.enable`, `totp.disable`, `recovery.regenerate`,
`invite.accept`, and now `password.change`) live outside that subtree, so they use
the store-only `auditz.Recorder` (ADR-0012) — Begin (intent) before, Finalize
(outcome) after. **`password.change`, unaudited since Phase 2, is audited now.**

## Consequences

- Tenant growth is self-service and tamper-proof; a stolen invite email grants
  only the exact pre-decided role, and only until it is used once or expires.
- A TOTP secret is useless in a raw DB dump (encrypted); a half-authenticated
  browser holds a token that can do nothing but finish its own login.
- Every security-relevant account change is now on the audit spine.
- New runtime deps: `pquerna/otp` (+ its `boombuler/barcode` QR dep). New config:
  `SMTP_*`, `INVITATION_TTL`, `LOGIN_CHALLENGE_TTL`, `TOTP_ISSUER`.
- One new migration (000004, `login_challenges`); the three feature tables were
  pre-created in 000001 and are used as-is.

## Alternatives considered

- **Signed (JWT/HMAC) invite carrying scope+role** — rejected: puts authority in
  a client-held blob, can't be revoked, and one signing-key slip escalates every
  invite. The DB-row-of-record is simpler and strictly safer.
- **Stateless signed interim TOTP token** — rejected for the ADR-0006 reasons
  (unrevocable, Verify-bug promotion risk). A stored challenge is revocable,
  countable (lockout), and cannot be mistaken for a session.
- **Reusing `sessions` with a `pending_totp` flag** — rejected: a single missed
  check in the hot session path would promote a half-auth to full auth. A
  separate table + separate cookie makes that failure structurally impossible.
- **Argon2id / per-code salt for recovery codes** — rejected: no meaningful gain
  for high-entropy single-use codes, and it forfeits the O(1) consume-by-hash
  that keeps login cheap and race-free.
- **Encrypt the whole `totp_secrets` row / use libsodium** — over-engineered;
  Go stdlib AES-256-GCM on the one secret column is the boring, sufficient choice.
- **Frontend generates the TOTP secret / QR** — rejected: the secret must be
  minted and encrypted server-side; the backend returns the `otpauth://` URI and a
  server-rendered QR so no secret is derived in the browser.
</content>
</invoke>
