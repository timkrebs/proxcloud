# Releasing Proxcloud

The one-page human view of how code reaches the homelab. Proxcloud ships onto
Tim's own Proxmox host (`pve01`) through a staged, health-gated, reversible
pipeline — a real website's delivery flow, scaled to one node. The authoritative
design lives in **ADR-0014** (workflow topology), **ADR-0015** (blue/green proxy)
and **ADR-0016** (smoke scope); this file is the operator's map.

## The pipeline at a glance

```
  PR ─▶ ci.yml            (ubuntu-latest · permissions: contents:read · UNTRUSTED code)
         backend · frontend · contract · docker-build · gitleaks
              │ conclusion=success  AND  head_branch = main | v*
              ▼  workflow_run
       publish.yml         (ubuntu-latest · packages:write · id-token:write)   ◀── built now
         build --target prod @ head_sha  (immutable 40-char commit, regex-validated)
         push  ghcr.io/timkrebs/proxcloud-backend   : <full-SHA>   (+ : <semver> on v*)
               ghcr.io/timkrebs/proxcloud-frontend  : <full-SHA>   (+ : <semver> on v*)
         SBOM (syft) → artifact   ·   cosign keyless sign (best-effort)   ·   NEVER : latest
              │ conclusion=success  ·  workflow_run
              ▼
       deploy.yml          (runs-on: [self-hosted, homelab] · serial group deploy-pve01)
         deploy-staging ─▶ smoke-staging ─▶ gate-production (reviewer: timkrebs)
              ─▶ deploy-prod (blue/green cutover) ─▶ smoke-prod ─▶ [auto-rollback on fail]
         writes state/last-cutover
              ▼
       soak.yml            (schedule hourly · shares group deploy-pve01 · soak-only key)
         stop retired color older than 24h · prune local images keeping last 10
```

Each arrow between workflows is a `workflow_run` edge: a separate run with its
own least-privilege `permissions:` and its own runner class. This is the trust
boundary — a fork PR can trigger `ci.yml` but can **never** reach `publish.yml`
or `deploy.yml`, which only fire on completed runs of the **base-repo** workflow
(ADR-0014 §2). `latest` is never a deploy source; deploy resolves the exact
`workflow_run.head_sha`, so what CI went green on is exactly what ships.

**What exists today:** `ci.yml`, `publish.yml`, `deploy.yml` (the CD wave —
staging → smoke → prod gate → blue/green cutover → prod smoke → auto-rollback,
plus a `v*` release job and an ntfy summary), and `soak.yml` (the hourly
soak/prune sweep — stops the retired color past 24h and prunes local images
keeping the last 10, via a dedicated soak-only SSH key). The `deploy/` on-guest
scripts remain the manual path for a from-scratch bring-up or an out-of-band fix.
Operator runbooks live in `docs/runbooks/` (release, hotfix, rollback,
staging-rebuild, disaster-recovery, failure-drills).

## Two rules that never bend

- **Immutable SHA, never `latest`.** Every image is tagged with the full 40-char
  git SHA (and, on a `v*` tag, the semver). `publish.yml` sets `flavor:
  latest=false`; nothing publishes or deploys `latest`. Rebuilding the same
  commit yields the same tag — images are immutable.
- **Rollback is a proxy switch, never a down-migration.** Prod keeps the old
  color warm; reverting is `caddy reload` back to it (ADR-0015). Down-migrations
  are dev-only. Migrations are expand → migrate → contract, backward-compatible
  for one version so the old color runs against the new schema during soak.

## Normal release — just merge to `main`

1. Open a PR. `ci.yml` runs the five required checks on untrusted code.
2. Get a CODEOWNERS review and merge. The push to `main` re-runs `ci.yml`.
3. On green, `publish.yml` fires automatically: it builds both images at the
   merge commit and pushes `ghcr.io/timkrebs/proxcloud-{backend,frontend}:<SHA>`.
4. `deploy.yml` (once wired) deploys staging, runs staging smoke, then **waits at
   the `production` gate** for approval.

No tag is needed for a normal release; `main` flows on its own up to the gate.

## Cutting a versioned release — push a `v*` tag

Tag a commit already on `main` (so it has already passed CI once):

```bash
git tag -a v1.4.0 <sha-on-main> -m "v1.4.0"
git push origin v1.4.0
```

The tag push re-runs `ci.yml`; on green, `publish.yml` additionally pushes the
`:v1.4.0` semver tag next to the `:<SHA>` tag and stamps `internal/version`
(`semver`) accordingly. On `v*`, the wave's `release` job generates a GitHub
Release changelog from the conventional commits.

## Hotfix — deploy a specific SHA out of band

A hotfix does **not** need a fresh merge. Once the fix commit is on `main` and
`publish.yml` has pushed its `:<SHA>` image, trigger the deploy directly:

```
Actions → deploy.yml → Run workflow → ref = <full-SHA or v-tag>
```

`deploy.yml`'s `workflow_dispatch` takes a `ref` input; it is format-validated
on the runner (`^[0-9a-f]{40}$` or `^v\d+\.\d+\.\d+`) and re-validated by the
ADR-0015 forced-command wrapper server-side. The expedited wave still runs
staging smoke and still stops at the `production` gate — a hotfix is faster to
start, not less gated.

## Approving prod — the button *is* the gate

`deploy-prod` runs in the GitHub **Environment `production`**, which requires a
review from **timkrebs**. When the wave reaches it, GitHub shows a "Review
pending" prompt on the run; approving it releases the environment's secrets
(`PROD_SSH_KEY`, `SMOKE_EMAIL`/`SMOKE_PASSWORD`) and lets the cutover proceed.
There is no separate deploy button — **the approval is the deploy to prod.** The
one-line ntfy summary records who approved.

> **One prod job, one approval.** GitHub re-prompts the required reviewer for
> *every* job that targets a protected environment. To keep "one approval = the
> deploy", `deploy.yml` runs the cutover, the public-URL smoke, and the
> auto-rollback as **one** `environment: production` job (`deploy-prod`) — the
> ADR-0014 §4 gate/deploy/smoke stages are its ordered steps.

## What a prod cutover does

1. **`pg_dump` snapshot** of the shared Postgres before any migration.
2. Pull the `:<SHA>` images and bring up the **idle** color (blue/green).
3. Run the one-shot **migrator** (expand/contract); its stdout is captured to the
   deploy log and job summary.
4. **Health-check the idle color on its loopback port, bypassing Caddy**
   (`/api/health` + assert `/api/v1/version` `.commit == <SHA>`). Any failure
   here aborts with the old color still live — no rollback needed.
5. **Atomic switch:** flip the `active.caddy` symlink + `caddy reload`
   (zero-drop, drains in-flight SSE/WS). Old color stays warm.
6. **`smoke-prod`** re-runs the smoke binary through the public URL. On failure:
   **automatic rollback** = flip the symlink back + reload → **high-priority**
   ntfy naming the failed assertion.

## Rollback

- **Bad live path caught by prod smoke** → automatic: proxy switches back to the
  warm old color in one reload. Realistic RTO ≈ the smoke window + one reload
  (single-digit minutes).
- **Caught later, by hand** → run `deploy/.../deploy.sh rollback` (or flip the
  symlink + `caddy reload`) — same primitive, either direction.
- **A DB restore is only justified** when a migration corrupted data that the
  expand/contract discipline should have prevented — never for a routine bad
  deploy. Procedure and the tested `make restore-drill` live in
  `docs/runbooks/disaster-recovery.md`.

## Where secrets live (and where they never do)

- **GitHub Environment `production` secrets:** `PROD_SSH_KEY` and the prod
  `SMOKE_EMAIL`/`SMOKE_PASSWORD` (session login, not a PAT) — only readable by the
  protected `production` environment (i.e. only after the reviewer approves).
  Staging uses repo-level `STAGING_SSH_KEY` + repo `SMOKE_EMAIL`/`SMOKE_PASSWORD`;
  `SSH_KNOWN_HOSTS` and `NTFY_URL` are repo secrets. Because the prod smoke job
  runs in the `production` environment, its `SMOKE_EMAIL`/`SMOKE_PASSWORD` (and the
  `SMOKE_*` **variables**) resolve to the environment-scoped values, so a staging
  credential can never act on prod. Non-secret deploy config (`STAGING_SSH_HOST`,
  `PROD_SSH_HOST`, `STAGING_BASE_URL`, `PROD_BASE_URL`, `SMOKE_TENANT`,
  `SMOKE_PROJECT`, `SMOKE_NODE`, `SMOKE_TEMPLATE`, `SMOKE_STORAGE`, `SMOKE_BRIDGE`,
  `SMOKE_VMID`, `PROD_REVIEWER`) are repo/environment **variables**, not secrets.
- **GHCR auth:** `publish.yml` uses only the built-in `GITHUB_TOKEN` (no PAT) via
  its `packages: write` scope; cosign uses the workflow's OIDC `id-token`.
- **App secrets never touch git or CI.** `PROXMOX_TOKEN_SECRET`, `SECRETS_KEY`,
  DB credentials live **only** in `/opt/proxcloud/.env` on each guest, created by
  hand. The deploy step templates non-secret config only. The self-hosted runner
  holds no app secrets — it only invokes the deploy wrapper over SSH.

## Supply chain: SBOM + signing

- **SBOM:** every published image gets a syft SPDX SBOM uploaded as a workflow
  artifact (`sbom-backend-<short>.spdx.json`, `sbom-frontend-<short>.spdx.json`).
- **cosign keyless signing is ENABLED, best-effort.** The sign step runs *after*
  the push and is `continue-on-error`, so a Sigstore/Fulcio/Rekor outage can
  never block an already-published, immutable image set (nor stall the
  `workflow_run` edge into deploy). Verify a signature with:

  ```bash
  cosign verify ghcr.io/timkrebs/proxcloud-backend@<digest> \
    --certificate-identity-regexp 'https://github.com/timkrebs/proxcloud/.github/workflows/publish.yml@.*' \
    --certificate-oidc-issuer https://token.actions.githubusercontent.com
  ```

  Because it is best-effort, a green publish does **not** guarantee a signature
  exists; treat verification as advisory hardening, not a gate. If Sigstore
  proves flaky against the homelab in practice, disable the two cosign steps in
  `publish.yml` and record the decision as an addendum to ADR-0014.

## Required CI checks (must be green to merge)

`backend` · `frontend` · `contract` · `docker-build` · `gitleaks`

`commitlint` is **warn-only** until 2026-08-26, then becomes required.
`publish.yml` / `deploy.yml` are **not** merge gates — they run post-merge and
gate *delivery*, not merge (ADR-0014 §8).

---

## One-time setup (do this once in the GitHub UI)

These are manual because they are account/repo settings, not code. Do them once
against `github.com/timkrebs/proxcloud`.

### 1. Branch protection on `main`

Settings → Branches → Add rule, pattern `main`:

- [ ] **Require a pull request before merging** (no direct pushes to `main`).
- [ ] **Require review from Code Owners** (CODEOWNERS already assigns `@timkrebs`).
- [ ] **Require status checks to pass**, and select all five:
      `backend`, `frontend`, `contract`, `docker-build`, `gitleaks`.
      (Optionally: "Require branches to be up to date before merging.")
- [ ] **Do not allow force pushes**; **do not allow deletions**.
- [ ] Keep "Include administrators" on for an honest, no-bypass `main`.

### 2. `production` Environment with a required reviewer

Settings → Environments → New environment → **`production`**:

- [ ] **Required reviewers** → add **`timkrebs`**. This is the prod gate; the
      approval is the deploy.
- [ ] Add environment **secrets** **`PROD_SSH_KEY`**, **`SMOKE_EMAIL`**,
      **`SMOKE_PASSWORD`** here (NOT as repo secrets) so they are only readable
      after approval — these are the **prod** smoke user's session credentials.
- [ ] Add environment **variables** for anything prod-specific: at minimum
      **`SMOKE_TENANT`**, **`SMOKE_PROJECT`**, **`SMOKE_VMID`** (a *separate* smoke
      tenant + reserved VMID from staging — ADR-0016 §4). `PROD_SSH_HOST`,
      `PROD_BASE_URL`, and the shared `SMOKE_NODE`/`SMOKE_TEMPLATE`/`SMOKE_STORAGE`/
      `SMOKE_BRIDGE` may stay repo-level.
- [ ] (Optional) Restrict deployment branches to `main` and `v*` tags.

Staging secrets (`STAGING_SSH_KEY`, staging `SMOKE_EMAIL`/`SMOKE_PASSWORD`),
`SSH_KNOWN_HOSTS`, `NTFY_URL`, and **`SOAK_SSH_KEY`** are repo secrets (or a
separate `staging` environment) — they are not prod-gated. `SOAK_SSH_KEY` is the
**dedicated soak-only** private key used by the unattended hourly `soak.yml`; its
public half (`ci-soak-key.pub`) must be placed on the prod guest at
`/opt/proxcloud/ci-soak-key.pub` before running `bootstrap.sh`, which installs it
with the `soak-wrapper.sh` forced command (soak-only; cannot deploy/rollback —
see `deploy/README.md` §6). Generate it like the deploy key:
`ssh-keygen -t ed25519 -N '' -f ci-soak-key -C proxcloud-soak`; store the private
key as `SOAK_SSH_KEY`, copy `ci-soak-key.pub` to the prod guest. The staging `SMOKE_*` **variables**
(`STAGING_SSH_HOST`, `STAGING_BASE_URL`, `SMOKE_TENANT`, `SMOKE_PROJECT`,
`SMOKE_NODE`, `SMOKE_TEMPLATE`, `SMOKE_STORAGE`, `SMOKE_BRIDGE`, `SMOKE_VMID`,
optional `PROD_REVIEWER`) are repo-level variables; the `production` environment
overrides only the ones that must differ. `SSH_KNOWN_HOSTS` = the output of
`ssh-keyscan <staging-host> <prod-host>` (one-time), so `StrictHostKeyChecking`
stays on. **The same `SMOKE_EMAIL`/`SMOKE_PASSWORD` must also be placed in each
guest's `/opt/proxcloud/.env`** so `proxcloud seed-smoke` creates a user whose
credentials match what the smoke job logs in with.

### 3. GHCR package visibility + repo link

After the first successful `publish.yml` run creates the two packages
(`proxcloud-backend`, `proxcloud-frontend`) under
`github.com/users/timkrebs/packages`, for **each** package:

- [ ] **Link it to the `proxcloud` repo** (Package settings → "Connect
      repository") so repo permissions and the deploy guests' pull access are
      inherited, and the OCI `source` label resolves.
- [ ] **Set visibility to match the repo** (public repo → public packages;
      private repo → keep private and provision a read-scoped pull credential in
      `/opt/proxcloud/.env` on each guest for `docker login ghcr.io`).

### 4. Coordinate (tracked in the ADRs, not blockers for publish)

- ntfy topic URL/token for the deploy summary line (ADR-0014 §6). `NTFY_URL`
  unset ⇒ the notify job no-ops gracefully.
- Lab domain(s) and whether prod sits behind the Cloudflare Tunnel — sets Caddy's
  TLS mode (ADR-0015 §6).
- `smoke` template ID + storage pool on `pve01`, the reserved VMID range, and the
  Proxmox token's `Pool.Allocate` grant for the smoke LXC create/delete
  (ADR-0016 §4).
- **Backend `proxcloud seed-smoke` command** (shipped alongside `proxcloud
  migrate`): creates the smoke tenant/project + a non-TOTP Contributor user from
  `SMOKE_EMAIL`/`SMOKE_PASSWORD`, idempotently. `deploy.sh` invokes it under
  `SMOKE_SEED=true` before the smoke gate (staging on; prod off by default). The
  smoke `SMOKE_EMAIL`/`SMOKE_PASSWORD` in each guest's `/opt/proxcloud/.env` must
  match the GitHub `SMOKE_EMAIL`/`SMOKE_PASSWORD` the smoke job logs in with, or
  `smoke-staging` fails at the login assertion.
- The self-hosted runner only needs to **run a static binary + `ssh`/`curl`** —
  the smoke binary is built on a hosted runner and downloaded as an artifact, so
  the runner LXC needs no Go toolchain.
