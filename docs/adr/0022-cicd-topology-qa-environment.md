# ADR-0022: CI/CD topology — add a QA environment

Date: 2026-08-27 · Status: accepted · Delivery/CD

## Context

The delivery pipeline (ADR-0014) ships onto one Proxmox host (`pve01`) through two
standing environments: **staging** — an LXC (`deploy/terraform/staging.tf`, VMID
8001), single-stack, disposable, wired from repo-level ungated secrets/vars — and
**production** — a VM (`prod.tf`, VMID 8002) running blue/green behind Caddy
(ADR-0015), gated by a protected `production` GitHub Environment with a required
reviewer. The wave today is `deploy-staging → smoke-staging → gate → deploy-prod`.

The pipeline-modernization reference architecture is **Source → Build → Test →
Release → QA → Staging → Production**. Proxcloud has Source/Build/Test/Release and
Staging/Production, but no **QA** stage: staging currently doubles as both the
first automated deploy target *and* the production-like pre-prod, conflating two
distinct intents. This ADR adds QA as a third standing environment and fixes where
it sits in the ADR-0014 wave, its trust model, and how it reuses the ADR-0016 smoke
binary. It does **not** change staging or prod, and it does **not** extend blue/green
(that stays prod-only, ADR-0015).

## Decision

### 1. QA = a third **standing** environment, an LXC clone of the staging pattern
QA is a long-lived LXC guest (`deploy/terraform/qa.tf`, **VMID 8003**,
`qa.proxcloud.lab`), a copy of the staging Terraform/on-guest pattern — the simple
root-SSH `remote-exec` provisioning path, **not** prod's VM/blue-green path. It is
**single-stack** (one compose project, one Postgres), **always-on but disposable**:
rebuildable from Terraform + a `deploy <sha>` at any time (see the QA rebuild
runbook), with **no rollback and no blue/green** — a failed QA deploy stops the wave,
it does not self-heal. QA carries its own compose/Caddyfile tree (`deploy/host/qa/`,
site `qa.proxcloud.lab`) and its own `proxcloud-qa-*` container/project names and
Postgres host, so it is fully isolated from staging and prod state.

### 2. Wave insertion — Registry → QA(auto) → Staging(auto) → Production(gate)
QA slots into the ADR-0014 §4 wave **ahead of** staging as the first deploy target,
making the ordered chain:

```
deploy-qa → smoke-qa → deploy-staging → smoke-staging → gate-production → deploy-prod → smoke-prod
```

`deploy-qa` (`needs: prepare`) and `smoke-qa` (`needs: [prepare, build-smoketest,
deploy-qa]`) mirror the existing staging jobs one-for-one; `deploy-staging` gains
`needs: smoke-qa`. Registry (publish, ADR-0014 §1) is unchanged and still fires only
on a green `ci` run; deploy resolves the same SHA-immutable image (never `latest`).
The production gate and everything downstream are untouched. The `notify` job's
`needs` list is extended to include the QA jobs so the ntfy summary names the QA
stage on failure.

### 3. Same serial group and same self-hosted trust model
`deploy-qa`/`smoke-qa` run on the same `[self-hosted, homelab]` runner and **join the
single serial `deploy-pve01` concurrency group** (ADR-0014 §3) — a wave still touches
QA → staging → prod strictly serially and is never interrupted. QA gets its **own
least-privilege deploy path** identical in shape to staging's (ADR-0014 §7,
ADR-0015): a dedicated forced-command SSH keypair (`keys/ci-deploy-qa`), its host
pinned in `SSH_KNOWN_HOSTS`, and **repo-level ungated** secrets/vars
(`QA_SSH_KEY`, `QA_SSH_HOST`, `QA_BASE_URL`) — QA is *not* a protected Environment
(only prod is). QA holds no prod credentials; its app secrets live only in the QA
guest's `/opt/proxcloud/.env`.

### 4. Reuse the ADR-0016 smoke binary unchanged, QA-scoped
`smoke-qa` runs the **same** `deploy/smoketest` Go binary (fully env-driven) against
`vars.QA_BASE_URL`, with a **QA-scoped smoke tenant/project/user and its own reserved
`SMOKE_VMID`, distinct from staging's and prod's** — ADR-0016 §4's "separate per
environment" rule extended to a third environment so a QA credential can never act on
staging or prod and a runaway QA smoke can never collide with their reserved VMIDs.
Failure semantics follow ADR-0016 §2's staging case: any QA smoke assertion fails →
non-zero exit → `smoke-qa` fails → the wave **stops before staging** → notify. No
rollback (QA is disposable).

### 5. The intent split — three environments, three jobs
The reason QA is *added* rather than folded into staging is that the two now have
**distinct, non-overlapping jobs**:
- **QA** — automated integration/smoke on a **fresh deploy**. First deploy gate:
  catches deploy/config/migration breakage early, on an environment cheap to rebuild
  and safe to break. This is where a bad image is caught before it reaches a
  production-like host.
- **Staging** — **production-like pre-prod verification**. Same smoke, but on the
  environment that most resembles prod, as the final automated check before a human
  is asked to approve.
- **Production** — **gated release** (human approval, blue/green, auto-rollback).

A green QA is the precondition for spending staging's (and a reviewer's) attention.

## Consequences

- The pipeline now matches the reference topology end-to-end: every SHA that reaches
  staging has already survived a full deploy + smoke on a throwaway environment, so
  staging failures are rarer and more meaningful, and the prod gate sees a build
  vetted twice.
- One more serial hop per wave: QA adds a `deploy + smoke` cycle ahead of staging, so
  a full green wave is longer end-to-end. Acceptable — the serial `deploy-pve01`
  group already makes waves queue, and QA catches the cheap failures first, before
  staging/prod time is spent.
- QA is disposable by construction: no rollback, no blue/green, no protected
  environment — its whole value is being safe to break and fast to rebuild, which
  keeps its maintenance cost near staging's rather than prod's.
- Blast radius stays split three ways: separate deploy keys, separate smoke
  tenants/creds, separate reserved VMIDs per environment (ADR-0016 §4).
- **Needs Tim / coordinate:** the QA LXC must be provisioned by `terraform apply`
  (VMID 8003, `qa.proxcloud.lab`) with its `keys/ci-deploy-qa` keypair installed and
  its host added to `SSH_KNOWN_HOSTS`; the QA `SMOKE_*` secrets + a reserved
  `SMOKE_VMID` distinct from staging/prod, and `QA_SSH_KEY`/`QA_SSH_HOST`/`QA_BASE_URL`,
  must be set as repo-level secrets/vars; the QA guest's `/opt/proxcloud/.env` must
  carry matching `SMOKE_EMAIL`/`SMOKE_PASSWORD` so `seed-smoke` (ADR-0016 §4) creates
  a user the smoke job can log in as.

## Alternatives considered

- **Ephemeral, per-deploy QA (create the LXC on each wave, destroy after smoke)** —
  rejected: adds moving parts to every wave (provision + teardown inside the serial
  `deploy-pve01` window, on the lone self-hosted runner) and makes a QA failure
  harder to inspect after the fact. A standing, rebuildable LXC gives a clean
  always-available progression at the cost of one idle guest — the homelab-right
  trade, consistent with how staging already runs.
- **QA as a CI integration stage only (no deployed guest)** — rejected: least
  faithful to the reference architecture, which places QA *after* Release as a
  deployed environment, and it would leave the first real-deploy signal at staging
  exactly as today. The integration *test* stage is a separate concern (ADR-0024);
  QA here is specifically a deployed smoke target.
- **Give QA its own blue/green + rollback** — rejected: duplicates prod's
  complexity (ADR-0015) on an environment whose entire purpose is to be disposable;
  a failed QA deploy should simply stop the wave, not self-heal.

See ADR-0014 (wave/topology/concurrency), ADR-0015 (prod-only blue/green), and
ADR-0016 (smoke scope + per-environment credentials/VMID).
