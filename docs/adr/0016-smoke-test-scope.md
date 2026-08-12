# ADR-0016: Smoke-test scope (`deploy/smoketest/`)

Date: 2026-08-12 · Status: accepted · Delivery/CD

## Context

The CD wave gates on a Go smoke binary (`deploy/smoketest/`): blocking against
**staging** (fail = stop the wave before the prod gate) and, after the blue/green
cutover, against **prod through the public URL** (fail = automatic rollback +
loud notify) — ADR-0014 §4. This ADR fixes exactly what the binary asserts, its
failure semantics, what is deliberately **out** of scope, and how the smoke
tenant/project/user/PAT are provisioned safely. It is a black-box **API contract +
liveness** check, not a test suite — its only job is "is *this* build actually
serving real traffic correctly end-to-end, against a real Proxmox path."

## Decision

### 1. Assertions, in order (fail-fast, cleanup always runs)
Run against a single `BASE_URL` (staging origin, or the public prod URL):
1. **Version** — `GET /api/v1/version`; assert `.sha == $SMOKE_EXPECT_SHA` (the
   deployed SHA, passed by the wave). First and cheapest: proves the intended build
   is the one actually answering.
2. **Login** — authenticate with the seeded, project-scoped test **PAT**
   (`$SMOKE_PAT`); assert `200` and a usable session/token. Exercises the real auth
   path without interactive TOTP/invitation flows.
3. **List resources** — `GET` the smoke tenant/project's resources; assert `200`
   and well-formed (may be empty). Exercises the tenant-scoped read path + authz.
4. **Create + delete a throwaway LXC** in the dedicated `smoke` project/tenant —
   the real async path: `POST` create → receive **UPID** → poll `/api/tasks/{upid}`
   until `OK` (bounded, e.g. 180s) → `DELETE` → poll its UPID to `OK`. Uses a
   **reserved VMID range** (e.g. 99000–99009) and a smoke-only template/storage
   (`$SMOKE_TEMPLATE`, `$SMOKE_STORAGE`) so it can never collide with or exhaust
   real capacity. This is the one assertion that touches Proxmox for real.
5. **SSE** — open `/api/events`; assert **≥1 metrics frame** arrives within N
   seconds (e.g. 20s); close. Proves the proxy's buffering-off + streaming path
   (ADR-0015 §5) works through the same edge users hit.

Cleanup (delete the throwaway LXC, incl. a **deferred** delete if a later step
fails) always runs so neither environment is littered — even on partial failure.

### 2. Failure semantics
- **Staging:** any assertion fails → non-zero exit → `smoke-staging` fails → wave
  **stops before the production gate** → notify. No rollback (staging is disposable).
- **Prod (through the public URL):** any assertion fails → non-zero exit →
  `smoke-prod` triggers **automatic rollback** (ADR-0015 symlink flip + `caddy
  reload` back to the old color) → **high-priority** ntfy naming the failed
  assertion. The go/no-go on the *code* was already made by the pre-switch idle
  health check (ADR-0015 §4); prod smoke validates the **live** path and reverts if
  it is wrong.
- Exit code is the contract; per-assertion pass/fail is emitted as a table into the
  job summary and the ntfy line (ADR-0014 §6).

### 3. Explicitly out of scope
- **No real user data** — only the `smoke` tenant/project is ever touched.
- **No destructive prod ops** beyond the smoke project's own throwaway LXC; never
  another tenant's guest, never a volume, never a migration, never a node/infra op.
- **No load/perf, chaos, or multi-node theater** — homelab-honest, single node.
- **No UI/render assertions** — that is frontend CI's job; smoke is API black-box.
- **No TOTP/invitation/interactive-auth flows** — covered by unit/contract CI; the
  PAT deliberately bypasses interactive login so smoke stays deterministic.

### 4. Safe provisioning of the smoke tenant/project/user/PAT
- **Idempotent seed**, guarded by `$SMOKE_SEED=true` (staging + prod-with-smoke
  only), run as/with the one-shot migrator step: **upserts** tenant `smoke`, project
  `smoke`, and user `smoke@proxcloud.local` bound to a **least-privilege role scoped
  to only the `smoke` project** — create/delete/list LXC there and nothing else (no
  platform-admin, no node/infra endpoints, no other tenant). The tenant-iron-rule
  404 boundary (CLAUDE.md) already prevents it from seeing anything else.
- The smoke tenant is **flagged as system/test** so it is excluded from normal
  tenant lists and given a **tiny dedicated quota** + the reserved VMID range +
  smoke-only storage/template, so a runaway smoke run cannot starve real tenants.
- **PAT lives only as a GitHub Environment secret** (`SMOKE_PAT_STAGING`,
  `SMOKE_PAT_PROD`, scoped to the protected `production` environment), injected into
  the smoke job's env — **never in git/CI files**. The seed is idempotent and does
  **not** rotate an existing PAT (so the secret stays valid); rotation is a manual
  runbook step. Prod and staging use **separate** smoke PATs/tenants so a staging
  credential can never act on prod.

## Consequences

- A green smoke means the exact deployed SHA authenticates a real user, reads the
  tenant surface, drives a **real** async Proxmox create/delete to completion, and
  streams SSE through the production proxy — the honest end-to-end signal the gate
  needs, with no mock data anywhere.
- Prod is self-healing on a bad live path: smoke failure reverts to the warm old
  color in one reload; realistic RTO ≈ the smoke window + one reload.
- The blast radius is bounded by construction: one least-privilege tenant, a
  reserved VMID range, a tiny quota, smoke-only storage — a smoke bug cannot touch
  real tenants or real capacity.
- **Needs Tim / coordinate:** the `smoke` template ID + storage pool must exist on
  `pve01` and the token needs the **Pool.Allocate** grant already noted as
  outstanding; backend-engineer must ship `/api/v1/version` (SHA/semver/build-time)
  and confirm PAT auth accepts a project-scoped token; the reserved VMID range must
  be agreed so nothing else claims it.

## Alternatives considered

- **Reuse the CI contract job (CRUD→404) as the prod smoke** — rejected: contract
  runs against a **mocked** Proxmox client in CI; smoke must hit the **real**
  Proxmox path and the **real** proxy/SSE edge. They test different things.
- **Skip the real LXC create; assert only health+version+list** — rejected: the
  create→UPID→poll path is the product's core promise (honest async task states);
  a smoke that never exercises Proxmox would pass while creates are broken.
- **A shared smoke tenant across staging and prod** — rejected: a staging PAT could
  then reach prod; separate tenants/PATs per environment keep the blast radius split.
- **Admin/superuser PAT for smoke** — rejected: violates least privilege and could
  let a smoke bug touch real resources; project-scoped is the safe minimum.
- **Run smoke as a container inside the wave** — acceptable but unnecessary; a
  static Go binary run by the self-hosted runner (staging) and the wave (prod) has
  no runtime deps and is the simplest reproducible artifact.
