# ADR-0023: Coverage-gate policy — ratchet at measured current

Date: 2026-08-27 · Status: accepted · Delivery/CD

## Context

CI measures coverage but does not enforce it. The backend runs a single
`go test -race -coverprofile=coverage.out ./...`; the frontend runs `vitest run`
with **no coverage provider at all**. The number exists (backend) or is absent
(frontend), and neither can fail a build. The pipeline-modernization wave's Build
stage needs a coverage gate that actually **fails the build below a floor**.

Two facts shape the policy. First, the codebase already carries dense table-driven
tests, so day-one coverage is not starting from zero — a fixed high bar risks
blocking current work, while no bar lets it rot. Second, `backend/internal/store`
coverage comes **only** from the DB-gated integration tests (ADR-0024): a unit-only
measurement would report `internal/store` as near-zero and collapse the backend
total, so the gate cannot threshold a unit-only profile. This ADR fixes the gate's
policy (what number, how it moves), where the number lives, and the Go
merge-profile mechanism that makes it honest.

## Decision

### 1. Policy = **ratchet at measured current**, never-decrease, target 85%
The implementer measures each stack's real current coverage first and sets the floor
**there, rounded down to a whole percent**. The gate then enforces **never-decrease**:
a PR that drops a stack below its floor **fails**. Raising a floor is a deliberate act
(§2). The documented **long-term target is 85%** for both stacks; the ratchet is the
mechanism that walks toward it without a flag-day.

Rationale: with tests already dense, a measured-current floor blocks *regression*
from day one (the real risk) and creates steady upward pressure, without failing the
very PR that introduces the gate. It is the honest homelab-scale choice — improvement
by ratchet, not by a fictional target imposed overnight.

### 2. One documented threshold location, raised only by a deliberate PR
The floors live in exactly one place: **`.github/coverage.env`**, holding
`COVERAGE_MIN_BACKEND=NN` and `COVERAGE_MIN_FRONTEND=NN`. This file is **sourced by CI
and by `make cover`** so a contributor's local `make cover` enforces the identical
number the gate does — no drift between local and CI. Raising a floor is a normal PR
that edits these two values (typically after a coverage-improving change lands and the
new real number clears the next whole percent); the ratchet is enforced by convention
+ review, not by machinery that mutates the file automatically.

### 3. Go gate thresholds the **complete-suite** profile (no separate merge)
Because `internal/store` is integration-covered only (ADR-0024), the gate must not
threshold a unit-only profile. The key observation: `go test -tags=integration ./...`
runs the **complete** suite — the DB-gated `//go:build integration` files **plus** all
the ordinary unit tests (which have no build constraint). So its `-coverpkg=./...`
profile already reflects what unit *and* integration tests together exercise — it **is**
the merged coverage, with no separate `gocovmerge`/merge job needed.

The `integration` job therefore both produces the authoritative profile and carries the
gate:
`go test -race -tags=integration -coverprofile=integration.cover -coverpkg=./... ./...`
against the Postgres service (ADR-0024) → `go tool cover -func` total → compare to
`COVERAGE_MIN_BACKEND`, **fail below**, and upload `integration.cover` as the
`backend-coverage` artifact. The separate `backend` (unit, no-`DATABASE_URL`) job still
runs — fast, DB-free — as an independent gate that catches accidental DB coupling and
gives quick feedback; it just doesn't need to contribute a second coverage profile.

### 4. Frontend gate via Vitest v8 thresholds
Add `@vitest/coverage-v8` and a `coverage` block in `frontend/vitest.config.ts` whose
`thresholds` is fed `COVERAGE_MIN_FRONTEND` (from `.github/coverage.env`), plus a
`test:coverage` script. Vitest's own threshold check fails the run below the floor;
the Build job invokes it. The tygo-generated `frontend/src/lib/api/generated/**` is
excluded from the denominator (generated code is not test material).

### 5. Reporting, and gate/permission placement
The merged Go report and the frontend report are published as artifacts and their
totals printed into `$GITHUB_STEP_SUMMARY` (ADR-0014 §6), so a failing gate names the
actual percentage vs the floor. The coverage/build/test jobs run untrusted PR code and
therefore hold **`permissions: contents: read`** and no infra credentials (ADR-0014
§7). Coverage is a `ci.yml` gate; because Release chains off a *successful* `ci` run
(ADR-0014 §2), a below-floor build provably cannot publish or deploy.

## Consequences

- Regression is blocked from the first commit that adds the gate, while the
  introducing PR itself stays green — the ratchet never fails on the current number.
- The floor only moves up, by an explicit reviewed edit to `.github/coverage.env`, so
  the bar's history is legible in git and the same number governs `make cover`
  locally and CI remotely.
- `internal/store` is measured honestly: its integration-only coverage counts toward
  the backend total via the merged profile, so the store is not penalized for being
  DB-covered and the gate can't be gamed by deleting DB tests.
- The gate depends on both the unit *and* integration jobs completing, coupling
  ADR-0024's split to the coverage number; a broken integration job fails the wave
  before coverage can be judged, which is the correct order.
- **Needs Tim / coordinate:** the implementer must run each stack once to read the
  current merged-profile total and commit the rounded-down floors into
  `.github/coverage.env`; adding `@vitest/coverage-v8` is a frontend dependency bump;
  `make cover` is a new root-`Makefile` target that sources the same file.

## Alternatives considered

- **A fixed floor now (e.g. 80% both stacks)** — rejected: a single flat bar either
  sits below current (buys nothing) or above it (blocks unrelated PRs on day one). A
  measured-current ratchet adapts per stack and never blocks the work in flight.
- **Codecov / Coveralls SaaS** — rejected: adds an external dependency, an account,
  and a token to a solo homelab project for a number we can compute in-repo with
  `go tool cover` + Vitest. Keeping the gate in-repo means no third-party outage or
  upload can block a merge, and the threshold logic is auditable in the workflow.
- **Threshold the unit profile only** — rejected: collapses `internal/store` to
  near-zero and makes the backend total meaningless; the merged profile (§3) is the
  only faithful measurement given ADR-0024's split.
- **Auto-bumping the floor to the observed value on every green main build** —
  rejected: a machine-mutated threshold produces churny commits and can ratchet on a
  fluke high reading, then block the next honest PR; a deliberate human edit keeps the
  bar intentional.

See ADR-0014 (ci gates / Release chaining / permissions) and ADR-0024 (the unit vs
integration split the merged profile depends on).
