# Runbook — Rebuild staging from scratch

Staging is **disposable**. It has no blue/green and no rollback — when it is
wedged (bad migration state, corrupted volume, drifted config), you do not repair
it, you rebuild it. Its only jobs are to catch a production-only config
regression before the prod gate and to host the blocking `smoke-staging`.

## Fast path — same guest, fresh stack

On the staging guest (`/opt/proxcloud`), destroy the stack and its data, then let
the next deploy rebuild it:

```bash
cd /opt/proxcloud
docker compose --env-file .env -p proxcloud-staging -f docker-compose.yml down -v
#   -v also drops the Postgres volume (proxcloud-staging-pgdata) — staging data
#   is throwaway. Keep .env; it is the manual, per-guest secret file.
```

Then re-run the last deploy (re-runs migrations from empty, re-seeds smoke):

```bash
# through the forced command, from CI/laptop:
ssh <staging-deploy-host> "deploy <last-known-good-SHA>"
# or on the guest:
/opt/proxcloud/bin/deploy.sh <SHA>
```

`deploy.sh` brings up Postgres, applies embedded migrations at backend boot (or
the migrator service if `USE_MIGRATOR_SERVICE=1`), runs `seed-smoke`
(`SMOKE_SEED=1`), then health-gates on `/api/v1/version == SHA`. Idempotent — safe
to re-run after a partial failure.

## Full path — reprovision the guest

If the guest itself is broken (kernel/Docker-in-LXC issues, disk):

1. `cd deploy/terraform && terraform taint` the staging resource (or
   `terraform destroy -target=...` then re-`apply`) — **Tim's authorized step**;
   this touches real `pve01`. See `deploy/README.md` §1.
2. First-boot re-runs `first-boot.sh` + `bootstrap.sh` (Docker, the locked
   `deploy` user, the forced-command `authorized_keys`, Postgres TLS cert).
3. **Re-place `/opt/proxcloud/.env` by hand** from `deploy/host/staging/env.example`
   (app secrets never enter git/CI). Ensure `SMOKE_EMAIL`/`SMOKE_PASSWORD` match
   the `smoke-staging` job's repo secrets, or the smoke login will fail.
4. Trigger a deploy (above). The first deploy builds the whole stack.

## Gotchas

- **`.env` is the one thing you must not lose** — it is per-guest and manual.
  Everything else (compose files, Caddyfile, bin/) comes from the tree at
  provision time.
- If `smoke-staging` fails on the **login** assertion right after a rebuild, the
  smoke user was not seeded — check `SMOKE_SEED=1` and that `SMOKE_EMAIL`/
  `SMOKE_PASSWORD` are set in `.env` (see `state/last-seed.log`).
- Staging deliberately runs `PROXCLOUD_ENV=production` + Postgres TLS so it mirrors
  prod's fail-closed config. Do not "simplify" it to non-TLS — that defeats its
  purpose.
