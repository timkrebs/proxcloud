# Runbook — Disaster recovery (full prod-guest loss)

The honest, single-node, single-runner recovery plan for losing the entire
`proxcloud-prod` VM. Two independent artifacts get you back: a **Proxmox
backup** (`vzdump`) of the VM, and the **`pg_dump` snapshots** taken before every
prod migration. Code is immutable in GHCR and re-pullable by SHA, so the only
irreplaceable thing is the **Postgres data**.

## What can and cannot be recovered

| Asset | Source of truth | Recoverable? |
|-------|-----------------|--------------|
| App code / images | GHCR `:<SHA>` (immutable) | Yes — re-pull |
| Compose / Caddy / scripts | git (`deploy/host/`) | Yes — re-provision |
| `/opt/proxcloud/.env` (app secrets) | **manual, per-guest — NOT in git** | Only from your password manager / Proxmox backup |
| Postgres data (tenants, users, audit) | shared `proxcloud-data` volume | From the VM backup **or** the latest `pg_dump` snapshot |

**The `.env` is the sharpest edge.** `PROXMOX_TOKEN_SECRET`, `SECRETS_KEY`, DB
password live only on the guest. Keep `SECRETS_KEY` in your password manager —
without it, secrets encrypted at rest (TOTP, etc.) cannot be decrypted even with
the data restored.

## Recovery order

### A. Whole-VM restore (fastest if the Proxmox backup is fresh)

1. On `pve01`: restore the latest `vzdump` of `proxcloud-prod`
   (`qmrestore <backup> <vmid>` or the PVE UI → Backup → Restore).
2. Boot it. `/opt/proxcloud/.env`, the Docker volumes, and the state come back
   with the VM. `deploy.sh` on next run re-pulls images by SHA.
3. Sanity-check: `up-infra.sh` (Postgres + Caddy), then a smoke or a
   `curl /api/v1/version`.

This restores data to the **backup's** point in time. If the backup is older than
the last migration, layer the newest `pg_dump` on top (path B step 3+).

### B. Rebuild the guest + restore Postgres from a snapshot

Use when there is no usable VM backup, or you want the newest data.

1. Reprovision `proxcloud-prod` via Terraform (`deploy/README.md` §1) — **Tim's
   authorized step**. First-boot runs `first-boot.sh` + `bootstrap.sh`.
2. **Re-place `/opt/proxcloud/.env`** by hand from `deploy/host/prod/env.example`
   (same `SECRETS_KEY` as before, from your password manager). `up-infra.sh` to
   bring up the shared Postgres + Caddy.
3. Copy the newest snapshot to the guest and restore it into the running
   Postgres — the **exact inverse of the snapshot `deploy.sh` writes**
   (`pg_dump | gzip`):

   ```bash
   # newest snapshot is /opt/proxcloud/data/snapshots/pre-deploy-*.sql.gz
   snap=$(ls -1t /opt/proxcloud/data/snapshots/pre-deploy-*.sql.gz | head -n1)
   gunzip -c "$snap" | \
     docker compose --env-file /opt/proxcloud/.env -p proxcloud-data \
       -f /opt/proxcloud/data/docker-compose.yml exec -T postgres \
       psql -U "$POSTGRES_USER" -d "$POSTGRES_DB"
   ```
4. Deploy the last-known-good SHA (`ssh <prod-deploy-host> "deploy <SHA>"`), which
   brings up a color, health-gates, and cuts over.
5. Verify: `/api/v1/version`, a login, `state/live-color`.

> Snapshots live **on the prod guest** (`data/snapshots/`, retain
> `SNAPSHOT_RETAIN`, default 14). If the guest is *gone* and you did not also copy
> snapshots off-box, you are relying on the Proxmox VM backup (path A). **Copy
> snapshots (or the whole VM backup) off `pve01`** — a snapshot that only exists
> on the box you lost is not a backup.

## The restore is proven, not assumed — `make restore-drill`

A restore procedure that has never run is fiction. `make restore-drill` exercises
the **exact** `pg_dump | gzip → drop → gunzip | psql` round-trip against a
throwaway Postgres (it never touches any `proxcloud` DB) and asserts the data
survives byte-for-byte (row count + checksum). Real run:

```
$ make restore-drill
2026-08-12T21:17:30Z [restore-drill] starting throwaway postgres:16-alpine container (proxcloud-restore-drill-49109) on 127.0.0.1:55433
2026-08-12T21:17:31Z [restore-drill] engine: docker (postgres:16-alpine)
2026-08-12T21:17:31Z [restore-drill] create scratch database 'restore_drill' (never touches 'proxcloud')
2026-08-12T21:17:31Z [restore-drill] seed schema + 1000 rows
2026-08-12T21:17:32Z [restore-drill] pre-snapshot:  count=1000 checksum=5aa14879f8bb492bd62f332c5310d7c1
2026-08-12T21:17:32Z [restore-drill] pg_dump | gzip  ->  snapshot.sql.gz   (the exact prod snapshot format)
2026-08-12T21:17:32Z [restore-drill] snapshot bytes: 25865
2026-08-12T21:17:32Z [restore-drill] simulate loss: DROP SCHEMA drill CASCADE
2026-08-12T21:17:32Z [restore-drill] confirmed: drill.rows is gone
2026-08-12T21:17:32Z [restore-drill] restore: gunzip | psql   (the DR restore step)
2026-08-12T21:17:33Z [restore-drill] post-restore:  count=1000 checksum=5aa14879f8bb492bd62f332c5310d7c1
2026-08-12T21:17:33Z [restore-drill] PASS: 1000 rows survived pg_dump -> drop -> restore, checksums identical
```

Run it in CI or by hand before you ever *need* it. It uses a `postgres:16-alpine`
container (the same image prod runs) when Docker is up, else a local `initdb`
cluster.

## Honest RTO / RPO (single node, single runner)

- **RPO** — data loss window: at worst since the **last `pg_dump` snapshot**
  (taken before each prod migration) or the **last Proxmox backup**, whichever is
  newer. Between migrations there is no continuous WAL archiving here — that is a
  deliberate homelab-size choice, not an accident. Deploys are infrequent, so the
  practical RPO is "since the last deploy or last nightly VM backup."
- **RTO** — time to serve again:
  - **Path A (VM restore):** ~15–30 min, dominated by `vzdump` restore time for
    a 64 GB disk on homelab storage.
  - **Path B (rebuild + snapshot restore):** ~30–60 min honest, dominated by
    Terraform reprovision + re-placing `.env` by hand + the snapshot restore.
    Longer if `pve01` itself needs attention or the runner LXC must be rebuilt.
- **No HA, no failover.** One node, one runner: if `pve01` is down, Proxcloud is
  down until it is back. That is the accepted shape of a homelab control plane —
  do not pretend otherwise. The mitigations that *do* matter here are: off-box
  copies of the VM backup + snapshots, and `SECRETS_KEY` in a password manager.

## Related

- `rollback.md` — a bad *deploy* is a proxy switch, not a DR event. Use DR only
  for real data loss or guest loss.
- `deploy/README.md` §1 — Terraform reprovision.
