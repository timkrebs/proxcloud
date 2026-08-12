# ADR-0006: Identity, server-side sessions & password hashing

Date: 2026-08-12 · Status: accepted

## Context

v1 authenticates a single env-var admin with a **stateless HMAC-signed
session cookie** (`internal/auth/session.go`). A stateless cookie cannot
be revoked, cannot express idle/absolute lifetimes independently, and
cannot be rotated on privilege change — all required once real users,
logout, and role changes exist. Passwords also need a modern hash, while
the migrated env admin arrives as a bcrypt hash we must keep honoring
during cutover.

## Decision

- **Server-side sessions in Postgres.** The existing `proxcloud_session`
  cookie (HttpOnly/Secure/SameSite=Lax, name and attrs unchanged) now
  carries an **opaque 256-bit random token**; only its hash is stored
  (`sessions.token_hash` unique). This replaces the stateless HMAC.
- **Independent lifetimes:** `last_seen_at` drives an idle timeout,
  `absolute_expires_at` a hard cap. Every request bumps `last_seen_at`.
  Logout = delete the row (`revoked_at`), so **revocation is real** and a
  session list / revoke-others UI is possible.
- **Session-id rotation on login and on privilege change** (new
  membership, role change, platform-admin grant) — a fresh token is issued
  and the old row invalidated, defeating session fixation. The session
  also carries `active_tenant_id` for the tenant switcher.
- **Argon2id** (`golang.org/x/crypto/argon2`, m=64MiB, t=3, p=2) with a
  self-describing encoded string (algo, params, salt, hash). A
  **`PasswordHasher` abstraction** exposes `Hash` and `Verify`; `Verify`
  also accepts **legacy bcrypt** and, on a successful bcrypt login,
  transparently **rehashes to Argon2id** and updates `password_algo`.

## Consequences

- Logout, "sign out everywhere", and forced re-auth on role change all
  become correct server-side operations; a stolen cookie is killable.
- The env admin's `ADMIN_PASSWORD_HASH` (bcrypt) is honored through
  cutover and self-upgrades to Argon2id on first login — no forced reset.
- Every authenticated request does one indexed session lookup + a
  `last_seen_at` write; acceptable, and the row cache is the natural place
  for later OIDC session linkage.
- The `PasswordHasher` interface is the seam an OIDC relying party slots
  behind later (ADR scope defers OIDC), so this choice does not close that
  door. TOTP secret encryption and recovery-code hashing (phase 5) build
  on the same identity tables but are out of scope here.
