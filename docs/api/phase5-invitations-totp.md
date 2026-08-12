# API contract — Phase 5 (invitations + TOTP + recovery codes)

Extends `docs/api/phase4-quotas-audit.md`. Shared types are sourced from
`backend/api/types` (tygo-regenerated to `frontend/src/lib/api/generated/types.ts`).
Error envelope unchanged: `{ "error": { code, message, pveMessage? } }`.
`{tenantId}`/`{projectId}`/`{invitationId}` are UUIDs; `{token}` is the opaque
invite token. Auth column: `Public` · `Authenticated` (session, no role) ·
`Owner`/`Reader` (effective role) · `Admin` (platform-admin). Ownership/tenant
misses → **404**; role denials within a joined tenant → **403**.

Two new cookies join `proxcloud_session` (ADR-0006):
- **`proxcloud_totp`** — HttpOnly, Secure, SameSite=Lax, `Path=/api/auth`,
  `Max-Age=LOGIN_CHALLENGE_TTL` (default 300s). Carries the opaque login-challenge
  token (stored only as SHA-256). Never accepted by `Authenticate`.

---

## Shared types (`backend/api/types/invitation.go`, `totp.go`)

```go
// ---- invitations ----

// CreateInvitationRequest — POST …/invitations (Owner). scopeType/scopeId
// select a tenant-scope or project-scope grant; the project (if any) must belong
// to {tenantId}. role ∈ owner|contributor|reader.
type CreateInvitationRequest struct {
	Email     string `json:"email"`
	ScopeType string `json:"scopeType"` // "tenant" | "project"
	ScopeID   string `json:"scopeId"`   // tenantId (tenant scope) or projectId (project scope)
	Role      string `json:"role"`      // "owner" | "contributor" | "reader"
}

// Invitation — a pending invite as an Owner sees it. NEVER carries the token.
type Invitation struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	ScopeType    string    `json:"scopeType"`
	ScopeID      string    `json:"scopeId"`
	ScopeLabel   string    `json:"scopeLabel"`   // resolved tenant/project display name
	Role         string    `json:"role"`
	InvitedBy    string    `json:"invitedBy"`    // inviter display name/email; "" if user gone
	ExpiresAt    time.Time `json:"expiresAt"`
	CreatedAt    time.Time `json:"createdAt"`
	Status       string    `json:"status"`       // "pending" | "expired" (accepted rows are not listed)
}

// InvitationDetails — public GET …/invitations/{token}. Enumeration-safe:
// an unknown/expired/used token returns 404, never these fields.
type InvitationDetails struct {
	Email           string    `json:"email"`           // the address the invite was sent to
	TenantName      string    `json:"tenantName"`
	ScopeType       string    `json:"scopeType"`
	ScopeLabel      string    `json:"scopeLabel"`
	Role            string    `json:"role"`
	ExpiresAt       time.Time `json:"expiresAt"`
	RequiresAccount bool      `json:"requiresAccount"` // true ⇒ no user for Email yet → collect displayName+password
	SignedInMatches bool      `json:"signedInMatches"` // true ⇒ caller is signed in AS Email → one-click attach
}

// AcceptInvitationRequest — POST …/invitations/{token}/accept.
// For a NEW account (RequiresAccount): displayName+password required.
// For an existing/attached account: both ignored.
type AcceptInvitationRequest struct {
	DisplayName string `json:"displayName"`
	Password    string `json:"password"`
}

// ---- TOTP + recovery ----

// TOTPEnrollResponse — POST …/totp/enroll. Secret is generated + stored ENCRYPTED
// and UNCONFIRMED server-side; nothing here reveals it in plaintext beyond the
// standard otpauth secret the user must key into their authenticator.
type TOTPEnrollResponse struct {
	OtpauthURI   string `json:"otpauthUri"`   // otpauth://totp/Proxcloud:email?secret=…&issuer=Proxcloud
	QrPngDataUri string `json:"qrPngDataUri"` // "data:image/png;base64,…" server-rendered
	ManualKey    string `json:"manualKey"`    // base32 secret, space-grouped for manual entry
}

type TOTPVerifyEnrollRequest  struct{ Code string `json:"code"` }
type TOTPDisableRequest       struct{ Password string `json:"password"` }
type RegenerateRecoveryRequest struct{ Password string `json:"password"` }

// RecoveryCodes — returned ONCE at enable and at regenerate; never retrievable again.
type RecoveryCodes struct {
	RecoveryCodes []string `json:"recoveryCodes"` // 10 × "XXXXX-XXXXX"
}

// ---- login second factor ----

// LoginResponse — POST /api/auth/login now always returns 200 with this body
// (contract change from the Phase-2 204). When TotpRequired, NO session cookie is
// set; a proxcloud_totp challenge cookie is set instead.
type LoginResponse struct {
	TotpRequired bool `json:"totpRequired"`
}

// LoginTOTPRequest — POST /api/auth/login/totp. code is a 6-digit TOTP OR a
// recovery code ("XXXXX-XXXXX"); the handler auto-detects by shape.
type LoginTOTPRequest struct{ Code string `json:"code"` }
```

`backend/api/types/auth.go` `Me` gains `recoveryCodesRemaining int` (unused-code
count, for the Settings “N codes left” line). No token/secret is ever in `Me`.

---

## Invitations — management (tenant-scoped, Owner)

### POST /api/tenants/{tenantId}/invitations — `Owner`
Body `CreateInvitationRequest`. Validates: `email` well-formed; `role` valid;
project scope’s `scopeId` is a project of `{tenantId}` (else **404**); an Owner may
grant at most their own effective role (no privilege escalation — a project Owner
cannot mint a tenant Owner). Creates the row (256-bit token, hashed), sends the
invite email via `Mailer` (accept link `FRONTEND_ORIGIN + /invite/{token}`).
Response **201** `Invitation`. Idempotency: a still-pending invite for the same
(email, scope) is **replaced** (old row deleted, new token issued) rather than
duplicated. Audited `invitation.create` (via `AuditOnMutation`; detail carries
`email`, `role`, `scopeType`). Rate-limited per-IP.

### GET /api/tenants/{tenantId}/invitations — `Owner`
Pending (unaccepted) invites for the tenant **and its projects** (one
`ListPendingInvitationsByScopes` call, no N+1), each `Status` computed
(`pending`/`expired`). Response `Invitation[]`. Never returns tokens.

### DELETE /api/tenants/{tenantId}/invitations/{invitationId} — `Owner`
Revokes (hard-deletes) a pending invite of this tenant; **404** if it is not this
tenant’s. Response **204**. Audited `invitation.revoke`.

## Invitations — accept (public)

### GET /api/auth/invitations/{token} — `Public`
Look up by SHA-256(token). Unknown / expired / already-accepted → **404**
`not_found` (generic; no enumeration). Valid → **200** `InvitationDetails`
(`RequiresAccount` = no user exists for the invite email; `SignedInMatches` = the
caller’s current session belongs to that email). Rate-limited per-IP.

### POST /api/auth/invitations/{token}/accept — `Public`
Body `AcceptInvitationRequest`. Per-IP rate-limit + bcrypt semaphore (new-user
Argon2id hashing). In ONE `WithTx`:
1. Re-read invite by hash; assert unexpired + unaccepted (else **404**/**409**).
2. Resolve the acceptor:
   - **Signed in** as the invite email → attach to that user.
   - **Signed in as a different email** → **409** `email_mismatch` (sign out first).
   - **Not signed in, user exists** for the email → require they sign in first:
     **409** `account_exists` (frontend routes to sign-in, returns to accept).
   - **Not signed in, no user** → create the user (`displayName` required;
     `password` validated to the 12-char floor); this address is bound from the
     invite (not client-chosen).
3. Create the membership from the **row’s** `scope_type`/`scope_id`/`role`.
4. Stamp `accepted_at` (`WHERE accepted_at IS NULL`; 0 rows → **409** raced).
5. Issue a fresh session (rotation) + set `active_tenant_id` to the invite’s
   tenant. Response **204** + `Set-Cookie: proxcloud_session`.
Audited `invitation.accept` (auditz; actor = the accepting user; tenant = invite
tenant). Accepting the token is treated as proof of mailbox control — no separate
email-verification step.

---

## TOTP + recovery codes (account-level, authenticated)

### POST /api/auth/totp/enroll — `Authenticated`
No body. Generates a secret (RFC-6238; issuer `TOTP_ISSUER`, account = email),
`Seal`s it, upserts `totp_secrets` **unconfirmed** (`confirmed_at` NULL, replacing
any prior unconfirmed secret). **409** `conflict` if TOTP is already enabled
(disable first). Response **200** `TOTPEnrollResponse`. Not audited (no security
state changed until confirm).

### POST /api/auth/totp/verify — `Authenticated`
Body `TOTPVerifyEnrollRequest`. Loads + `Open`s the unconfirmed secret; validates
`code` (±1 step). On success, in ONE `WithTx`: `confirmed_at=now()`,
`users.totp_enabled=true`, replace recovery codes with 10 fresh ones. Response
**200** `RecoveryCodes` (shown once). Bad code → **401** `unauthenticated`; no
pending secret → **409**. Rate-limited (per-IP + per-account). Audited
`totp.enable`.

### POST /api/auth/totp/disable — `Authenticated`
Body `TOTPDisableRequest` (**password re-prompt**, bcrypt semaphore). On correct
password, in ONE `WithTx`: delete `totp_secrets`, delete `recovery_codes`,
`users.totp_enabled=false`. Response **204**. Wrong password → **401**. Audited
`totp.disable`.

### POST /api/auth/totp/recovery-codes — `Authenticated`
Body `RegenerateRecoveryRequest` (**password re-prompt**). Requires TOTP enabled
(**409** otherwise). Replaces all recovery codes with 10 new ones. Response **200**
`RecoveryCodes` (shown once). Audited `recovery.regenerate`.

---

## Login second factor

### POST /api/auth/login — `Public` (modified)
Unchanged request (`LoginRequest`). On correct password:
- **TOTP disabled** → issue session as today, but respond **200**
  `LoginResponse{ totpRequired:false }` + `proxcloud_session` cookie
  (contract change: was 204).
- **TOTP enabled** → do **not** issue a session. Create a single-use
  `login_challenge` (hashed token, bound `user_id`, `LOGIN_CHALLENGE_TTL`);
  respond **200** `LoginResponse{ totpRequired:true }` + `proxcloud_totp` cookie.
Bad credentials still **401** `unauthenticated` (unchanged, timing-flat). Per-IP
rate-limit unchanged.

### POST /api/auth/login/totp — `Public`
Reads the `proxcloud_totp` cookie → challenge by hash. Missing/expired/consumed →
**401** `totp_challenge_expired` (frontend restarts sign-in). Body
`LoginTOTPRequest`; the handler auto-detects TOTP (6 digits) vs recovery code:
- **TOTP**: `Open` the user’s confirmed secret, validate ±1 step.
- **Recovery**: SHA-256(normalize) → `ConsumeRecoveryCode` (atomic single-use).
On success: consume the challenge, issue the real session (rotation), set
`active_tenant_id`, clear `proxcloud_totp`. Response **204** + `proxcloud_session`.
On failure: increment the challenge’s attempt counter; at the 5th failure the
challenge self-consumes → **401** `totp_challenge_expired` (restart). Otherwise
**401** `unauthenticated`. Per-IP rate-limit + per-account lockout (via the
challenge counter). Audited `totp.login` (auditz; success/denied outcome).

---

## Permission-registry additions (`internal/authz/permissions.go`)

Appended to the set the completeness test asserts:

```
POST   /api/auth/login/totp                                    Public
GET    /api/auth/invitations/{token}                           Public
POST   /api/auth/invitations/{token}/accept                    Public
POST   /api/auth/totp/enroll                                   Authenticated
POST   /api/auth/totp/verify                            Authenticated
POST   /api/auth/totp/disable                                  Authenticated
POST   /api/auth/totp/recovery-codes                           Authenticated
GET    /api/tenants/{tenantId}/invitations                     Owner
POST   /api/tenants/{tenantId}/invitations                     Owner
DELETE /api/tenants/{tenantId}/invitations/{invitationId}      Owner
```

## Audit action-map additions (`internal/authz/audit_actions.go`)

Only the two tenant-scoped mutating routes flow through `AuditOnMutation`; they
MUST be in the action-map or the audit-completeness test fails:

```
POST   …/invitations                          invitation.create
DELETE …/invitations/{invitationId}           invitation.revoke
```

The account-level actions (`invitation.accept`, `totp.enable`, `totp.disable`,
`totp.login`, `recovery.regenerate`, `password.change`) are written by the
`auditz.Recorder` inside their handlers, not by the middleware.
</content>
