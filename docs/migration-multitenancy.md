# Runbook: upgrading a single-admin install to multi-tenancy

This runbook takes an existing Proxcloud v1 deployment (single env-var
admin, no database) to the multi-tenant platform (ADRs 0005–0010) without
losing the running guests on `pve01`. Read ADR-0005 and ADR-0006 first.

## 0. Before you start

- Take a snapshot/backup of the Proxmox host as usual; the upgrade does
  **not** touch guest disks, but back up anyway.
- Note your current `ADMIN_USER` and `ADMIN_PASSWORD_HASH` (bcrypt) — they
  become the first platform-admin.
- Nothing here deletes Proxmox resources. The reconciler never
  auto-deletes (ADR-0010).

## 1. Add Postgres

- Bring up the new `postgres` docker-compose service (see
  `docs/deployment.md` / `docker-compose.yml`). Use a persistent volume.
- The database is the new system of record; treat its volume as critical
  state and add it to your backup routine.

## 2. Set the new environment

Add to your `.env` (see `.env.example`):

- `DATABASE_URL` — pgx connection string to the Postgres service.
- `SECRETS_KEY` — a 32-byte key (base64) for AES-256-GCM encryption of
  TOTP secrets. Generate once and keep it secret; losing it invalidates
  encrypted secrets.
- Keep `ADMIN_USER` and `ADMIN_PASSWORD_HASH` set for the seed step below.
- Optional: session idle/absolute TTLs, reconciler interval, SMTP\*.

## 3. What the first boot does

On startup the backend runs embedded `golang-migrate` migrations before
serving (ADR-0005). If they fail, the server refuses to serve — fix the
DB and restart; this is fail-closed by design.

**Migration 001 (SQL, Phase 1)** creates all tables (schema only) and seeds
a `default` **tenant** and a `default` **project** (pool id
`pc-default-default`). It does **not** seed any user — a SQL migration can't
read env, and the admin conversion needs `ADMIN_USER`/`ADMIN_PASSWORD_HASH`.

**Go bootstrap (Phase 2/3, post-migration, idempotent)** — runs after
migrations on startup and does the env-dependent + Proxmox-touching work:
- converts the env admin into the **first platform-admin user** from
  `ADMIN_USER` + `ADMIN_PASSWORD_HASH` (`password_algo='bcrypt'`,
  `is_platform_admin=true`) with an Owner membership on the default tenant
  (Phase 2);
- ensures pool `pc-default-default` exists;
- reads `ClusterResources` and, for every qemu/lxc without an ownership
  row, inserts an **`active`** ownership row into the default project and
  best-effort adds it to the pool. PVE hiccups are logged, **never fail
  startup** — so your existing guests are backfilled before any scoping is
  enforced and the platform never 404s its own resources (Phase 3).

## 4. Env-admin cutover

The bcrypt env admin keeps working **until the first real user is
created** through bootstrap/invite. After that the env admin is
**disabled and the event logged loudly**. Recommended sequence:

1. Log in once as the env admin to confirm the upgrade.
2. Use the first-run **bootstrap** screen (or the members UI) to create a
   real platform-admin with an Argon2id password.
3. On that first real login the bcrypt hash self-upgrades to Argon2id
   (ADR-0006); the env admin is retired.
4. Remove `ADMIN_PASSWORD_HASH` from the environment once real admins
   exist.

## 5. Verify

- Migrations applied and are idempotent (re-running boot is safe).
- Every existing `pve01` guest appears under the **default** tenant/project
  and in pool `pc-default-default`, with no data loss.
- Log in, list resources (scoped), and create/start/stop/delete a test
  LXC as a non-admin Contributor in a **non-default** tenant; confirm a
  second tenant 404s on that vmid.
- Console and existing flows still work (regression).

## 6. Rollback

The upgrade is additive to Proxmox — no guest state changes — so rollback
is a control-plane rollback:

1. Redeploy the previous Proxcloud backend image (stateless HMAC sessions,
   no DB dependency).
2. Restore the previous `.env` (env admin only; `DATABASE_URL` /
   `SECRETS_KEY` unused by v1).
3. Leave the Postgres service and volume in place — v1 ignores it, and it
   preserves any tenancy data if you retry the upgrade.
4. Guests on `pve01` are unaffected. The `pc-*` pools created during the
   trial are harmless and can stay or be removed manually.

Do **not** roll back by deleting Proxmox pools or guests — the migration
never required destructive changes, and neither does undoing it.
