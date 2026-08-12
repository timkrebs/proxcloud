# Runbook — Normal release (the wave)

Operator view of a routine merge-to-prod. Authoritative design: ADR-0014 (CD
topology), ADR-0015 (blue/green Caddy), ADR-0016 (smoke). One-pager: `RELEASING.md`.

## What triggers it

A push to `main` (a merged PR). No tag is needed. `ci.yml` (untrusted, hosted) →
on green `publish.yml` builds + pushes `ghcr.io/timkrebs/proxcloud-{backend,frontend}:<40-hex-SHA>`
→ on success `deploy.yml` runs the wave on the self-hosted runner.

## The wave, stage by stage

| Stage | Where | What proves it | On fail |
|-------|-------|----------------|---------|
| `deploy-staging` | prod-less staging guest | `/api/health` up + `/api/v1/version` `.commit == SHA` | stop, ntfy |
| `smoke-staging` | staging public URL | ADR-0016 black-box smoke (version, login, list, real LXC create/delete, SSE) | **stop before prod gate**, ntfy |
| `deploy-prod` gate | GitHub Env `production` | required reviewer **timkrebs** approves — the approval *is* the deploy | waits |
| cutover (same job) | prod guest | `pg_dump` snapshot → migrate idle → loopback health (bypass Caddy) → atomic `active.caddy` flip + `caddy reload` | abort with old color still live |
| `smoke-prod` (same job) | public prod URL | same smoke through the live edge | **auto-rollback** + high-prio ntfy |
| `release` | hosted | on `v*` only: changelog → GitHub Release | — |

deploy-staging additionally runs the idempotent `seed-smoke` (SMOKE_SEED=1 on
staging) so the smoke user exists before `smoke-staging` logs in.

## What you actually do

1. Merge the PR. Watch Actions.
2. When the run reaches **deploy-prod**, GitHub shows "Review pending". Read the
   step summary (migrator tail + staging `/api/v1/version`). If it looks right,
   **Approve** — that releases the `production` secrets and runs the cutover.
3. Watch `smoke-prod`. Green ⇒ the wave posts a `LIVE` line to ntfy with the
   version, `blue→green` direction, and who approved. Red ⇒ it auto-rolled back
   (see `rollback.md`) and posts a high-priority line.

## Verify after

- Public URL serves the new build: `curl -fsS https://<prod>/api/v1/version` →
  `.commit` == the deployed SHA.
- `state/live-color` and `state/last-cutover` on the prod guest reflect the new
  color + timestamp (this is what `soak.yml` reads 24h later to retire the old
  color).

## Homelab-honest notes

- **One runner, serial waves.** `deploy.yml` uses `concurrency: deploy-pve01,
  cancel-in-progress:false`. A second merge queues behind the first — expected.
- **The old color stays warm** until the 24h soak sweep (`soak.yml`) stops it, so
  rollback is a single `caddy reload` for that whole window.
- For a versioned release, push a `v*` tag on a commit already on `main` (see
  `RELEASING.md` §"Cutting a versioned release"); the wave is identical plus the
  `release` job.
