# ADR-0014: CD workflow structure (GitHub Actions topology)

Date: 2026-08-12 · Status: accepted · Delivery/CD

## Context

Proxcloud ships onto Tim's own Proxmox host (`pve01`): staging = an LXC, prod = a
VM running blue/green compose behind a reverse proxy (ADR-0015). CI must run on
**untrusted PR code** and therefore must never touch homelab infra; delivery must
be **staged, health-gated, and reversible** (release-engineer.md). The homelab has
**one** self-hosted runner. This ADR fixes the GitHub Actions topology: which
workflows exist, how they chain, concurrency, the hosted/self-hosted boundary, the
wave's pass/fail/rollback semantics, and how migrator/version/smoke results surface.

## Decision

### 1. Four workflows, split by trust boundary and permission scope
- **`ci.yml`** — trigger: `pull_request` + `push` to `main` + `push` `v*` tags.
  Runs entirely on **`ubuntu-latest`**. Jobs (parallel, joined by `needs:`):
  `backend` (build/vet/staticcheck/`test -race`+coverage/govulncheck),
  `frontend` (ci/lint/typecheck/test/build), `contract`
  (CRUD→404 per resource type against a compose backend + a Postgres **service
  container** + the mocked Proxmox client), `docker-build` (buildx both images,
  **no push** on PRs). `permissions: contents: read` only.
- **`publish.yml`** — trigger: **`workflow_run`** on `ci.yml` `completed`, filtered
  to `conclusion == success` and (`head_branch == main` **or** a `v*` ref). Builds
  and pushes to GHCR (ADR: multi-stage, distroless/slim, non-root, healthcheck, OCI
  labels, SBOM via syft, cosign keyless stretch). Tags **git SHA + semver only;
  never `latest`**. Backend gets SHA/semver/build-time via `-ldflags`.
  `permissions: contents: read, packages: write, id-token: write`.
- **`deploy.yml`** — trigger: **`workflow_run`** on `publish.yml` success (auto
  main deploy) **or** `workflow_dispatch` with a `ref` input (hotfix by SHA/tag).
  Deploy jobs `runs-on: [self-hosted, homelab]`.
- **`soak.yml`** — trigger: `schedule` (hourly) + `workflow_dispatch`. Self-hosted.
  Stops colors past their 24h soak and prunes images (keep last 10). See §5.

### 2. `workflow_run` between workflows, `needs:` within one
`needs:` couples jobs that share **one** trigger event and trust level (all CI
jobs; all deploy-wave jobs). Crossing a **trust or permission boundary** — hosted
CI → GHCR-writing publish → self-hosted deploy — uses **`workflow_run`** so each
stage is a separate run with its own least-privilege `permissions:` and its own
runner class. This is the mechanism that keeps `packages: write` and the
self-hosted runner off every PR: a fork PR can trigger `ci.yml` but can never
reach `publish.yml`/`deploy.yml`, which only fire on completed runs of the
**base-repo** workflow. Deploy resolves the exact image by the
`workflow_run.head_sha` (immutable tag), never by branch/`latest`.

### 3. Concurrency
- `ci.yml`: `group: ci-${{ github.ref }}`, `cancel-in-progress: true` (drop
  superseded PR pushes).
- `publish.yml`: `group: publish-${{ github.sha }}`, `cancel-in-progress: false`
  (never abort a half-pushed image set).
- `deploy.yml`: **single serial** `group: deploy-pve01`, `cancel-in-progress:
  false` — waves **queue**, a cutover is never interrupted (a cancelled reload
  leaves the proxy half-switched). `soak.yml` shares `group: deploy-pve01` so
  cleanup never races a live wave.

### 4. The wave (deploy.yml jobs, ordered by `needs:`)
1. **`deploy-staging`** (self-hosted → SSH forced-command, ADR-0015): pull images
   by SHA → one-shot **migrator** service → `compose up` → wait
   `/api/health` + assert `/api/v1/version` **== deployed SHA**. Fail → stop,
   notify. Staging has no rollback (disposable).
2. **`smoke-staging`** (blocking): run the `deploy/smoketest` Go binary against
   staging (ADR-0016). Fail → stop the wave before the prod gate, notify.
3. **`gate-production`**: a job with `environment: production` (required reviewer
   **timkrebs**). The approval **is** the button-press to prod; secrets
   (`PROD_SSH_KEY`, `SMOKE_PAT_PROD`) are scoped to this protected environment.
4. **`deploy-prod`** (blue/green cutover): pre-migration **`pg_dump` snapshot** →
   deploy SHA to the **idle** color → migrate **expand/contract** (backward-
   compatible one version) → health-check the idle color **bypassing the proxy**
   (loopback port) → **atomic proxy switch** → keep old color **warm**. Any failure
   **before** the switch aborts with the old color still live (no rollback needed).
5. **`smoke-prod`**: re-run smoketest **through the public prod URL**. Fail →
   **automatic rollback** = proxy switch back + reload → **loud** high-priority
   notify. Success ends the wave and writes a `state/last-cutover` marker (color +
   timestamp) for `soak.yml`.
6. **`release`** (only on `v*`): generate a GitHub Release changelog from
   conventional commits.

Rollback in prod is **only** a proxy switch (code) — never a down-migration
(release-engineer.md); down-migrations are dev-only.

### 5. Soak/cleanup decoupled from the wave
A job cannot idle 24h cheaply, and the lone self-hosted runner must not be pinned.
So the wave **ends warm**: `deploy-prod` records the just-retired color + a
timestamp in `state/last-cutover`. `soak.yml` (hourly) stops any retired color
older than 24h and prunes images keeping the last 10. This makes the 24h soak a
data marker, not a running job.

### 6. Surfacing migrator/version/smoke output
Every deploy job appends markdown to **`$GITHUB_STEP_SUMMARY`**: the captured
migrator stdout (tail), the raw `/api/v1/version` JSON of the deployed color, and a
smoke-assertion pass/fail table (ADR-0016). A final `notify` job (`if: always()`)
posts one line to the **ntfy** topic: environment, `blue→green` (or the reverse on
rollback), version SHA, one-line migrator status, smoke result, and who approved.
Failure/rollback posts at high priority with the failing stage named.

### 7. Security posture
- **No untrusted `github.event.*` in `run:`** — inputs (the `workflow_dispatch`
  `ref`) pass through `env:` and are **format-validated on the runner**
  (`^[0-9a-f]{40}$` or `^v\d+\.\d+\.\d+`) before use; the ADR-0015 forced-command
  wrapper re-validates server-side.
- **Actions pinned by commit SHA**, not moving tags.
- **Least-privilege `permissions:`** per workflow (default `contents: read`; only
  `publish.yml` gets `packages`/`id-token`).
- Self-hosted runner is **repo-scoped, `--ephemeral`, in its own LXC** (not the
  prod guest); it holds no app secrets — those live only in `/opt/proxcloud/.env`
  on each guest (ADR-0015). The runner only ever runs already-merged, already-
  published code and only invokes the deploy wrapper over SSH.

### 8. Branch protection / required checks
`main` is protected: PR required, no direct push. Required status checks = the four
**`ci.yml`** jobs (`backend`, `frontend`, `contract`, `docker-build`).
`publish.yml`/`deploy.yml` are **not** required checks — they run post-merge and
gate delivery, not merge.

## Consequences

- Untrusted CI never executes on homelab infra and never holds GHCR/deploy
  credentials; the trust boundary is a workflow boundary, enforced by `workflow_run`.
- Every prod cutover is preceded by a passing staging smoke and a human approval,
  and is reversible by a single proxy switch with the old color still warm.
- Deploy waves are strictly serial and uninterruptible; a queued wave is normal.
- One-runner, single-node honest: no idle 24h jobs, no runner pinning; soak is a
  scheduled sweep. Realistic RTO for a bad prod deploy ≈ the smoke-prod window +
  one reload (single-digit minutes), because rollback is a symlink flip.
- **Needs Tim / coordinate:** `/api/v1/version` does not exist yet (current surface
  is `/api/health` under `/api`) — backend-engineer must add it before this wave is
  real; the ntfy topic URL/token and the GitHub Environment `production` reviewer
  rule are one-time manual setup.

## Alternatives considered

- **One mega-workflow with `needs:` across build→publish→deploy** — rejected: a
  single run can't cleanly straddle hosted and self-hosted runners with different
  `permissions:`, and it would grant `packages: write`/deploy reach to PR runs.
- **`repository_dispatch`/manual chaining instead of `workflow_run`** — rejected:
  `workflow_run` gives native "completed+success on this branch/tag" gating and
  base-repo-only execution for free; custom dispatch is more code and more foot-guns.
- **A long-lived job that sleeps 24h for soak** — rejected: pins the lone runner
  and burns minutes; a scheduled sweep over a state marker is the homelab-right size.
- **Deploying by `latest`/branch tag** — rejected outright by the immutability rule;
  deploy resolves the exact `head_sha` so what was smoke-tested is what ships.
