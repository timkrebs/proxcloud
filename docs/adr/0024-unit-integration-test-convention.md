# ADR-0024: Unit/integration test convention — `//go:build integration`

Date: 2026-08-27 · Status: accepted · Delivery/CD

## Context

The backend test suite has no unit/integration split. Everything runs in one
`go test -race -coverprofile=coverage.out ./...`; the DB-dependent tests in
`backend/internal/store/` **self-skip at runtime** via `requireStore` when
`DATABASE_URL` is unset (and gate destructive work behind
`PROXCLOUD_ALLOW_DESTRUCTIVE_TESTS`). That works, but it means a "unit" run still
compiles every DB test and, in CI, still spins a Postgres service so those tests
don't skip — the interim `contract` CI job even stands up Postgres for handler/
httpserver tests that actually use **in-memory fakes**, paying for a database that
those tests never touch. The pipeline-modernization Test stage wants a genuine
unit/integration boundary: a fast, DB-free unit run and a separate DB-backed
integration run, along the convention the reference names
(`go test -tags=integration`). This ADR fixes that boundary, which files move, and
what deliberately stays unit.

## Decision

### 1. Split by Go build tag — `//go:build integration` on the DB-dependent files
Add `//go:build integration` (with the matching blank line) to the **11
DB-dependent test files** in `backend/internal/store/`:

```
postgres_test.go              postgres_auth_test.go        postgres_ownership_stale_test.go
postgres_phase3_test.go       postgres_quota_test.go       postgres_quota_race_test.go
postgres_security_test.go     jobs_postgres_test.go        schedules_postgres_test.go
ttl_postgres_test.go          migrations_reversible_test.go
```

These are the tests that require a live Postgres. Build-tagging (rather than
runtime-skipping) means they **do not even compile** in a unit run — the unit run is
truly pure and fast, with no DB driver, no `requireStore` skip noise, and no
possibility of a DB test accidentally running without its tag.

### 2. Unit = `go test ./...` (no `DATABASE_URL`, no Postgres)
The default, untagged build is the **unit** suite: `go test -race ./...` with **no
`DATABASE_URL`**. The tagged files are excluded from compilation, so what remains is
pure or fake-backed and runs anywhere with zero services. This is the run the
coverage `build` job executes for `unit.cover` (ADR-0023 §3).

### 3. Integration = `go test -tags=integration ./...` with Postgres
The **integration** suite is `go test -race -tags=integration ./...` against the
Postgres 16 service container and the existing CI env block —
`PROXCLOUD_ALLOW_DESTRUCTIVE_TESTS=1` and the `proxcloud_ci_test` database — reused
verbatim from today's `ci.yml`. With the tag set, the untagged unit tests **and** the
tagged store tests compile and run together, so integration is a superset build that
additionally exercises the real store. This is the run that produces
`integration.cover` for the merged coverage gate (ADR-0023 §3).

### 4. What deliberately **stays** unit
`quota_unit_test.go` and every **fake-backed** test stay untagged and run in the unit
suite — specifically the handler tests, the httpserver tests, and the `internal/authz`
completeness test (the tenancy-iron-rule route-coverage check, CLAUDE.md). These use
in-memory fakes / the mocked `ProxmoxClient`, need no database, and must keep running
on every PR with no services. The tag marks *"requires Postgres,"* not *"is an
integration test in spirit"* — only DB dependence earns the tag.

### 5. Supersedes the interim `contract` CI job
The `integration` job **replaces** the `contract` job (ADR-0014 §1). `contract` stood
up Postgres to run handler/httpserver tests that use in-memory fakes — a database
those tests never read. Under this split those tests are plainly **unit** (they run in
the DB-free unit job), and the only tests that legitimately need Postgres are the
tagged store tests, which the `integration` job runs. Net: one honest DB job instead
of a mislabeled one, and the unit job stops paying for a database it never used.

### 6. The deployed-env black-box CRUD contract is smoke's job, not CI's
CI has **no real Proxmox** (untrusted PR code must never reach homelab infra, ADR-0014
§7), so CI cannot assert the real create→UPID→poll→delete contract. That
deployed-environment black-box CRUD contract is served by the **smoke suite**
(ADR-0016) against QA/staging/prod, not by any CI job. Retaining that boundary is why
dropping `contract` from CI loses no real coverage: the mocked-Proxmox contract it ran
is redundant with the handler unit tests, and the *real* contract was never CI's to
run.

## Consequences

- The unit run is genuinely fast and hermetic: no `DATABASE_URL`, no Postgres, DB
  tests not even compiled — it runs identically on a laptop and on `ubuntu-latest`,
  and a red unit job means a real defect, not a missing service.
- The convention is grep-able and opt-in: `rg '//go:build integration'` enumerates
  exactly the DB-dependent files, and a new store test is integration only if its
  author adds the tag — explicit by construction.
- CI drops a redundant Postgres spin-up (the old `contract` job) and gains one honest
  `integration` job; the two coverage profiles this split produces feed the merged
  coverage gate (ADR-0023).
- The `requireStore`/`PROXCLOUD_ALLOW_DESTRUCTIVE_TESTS` runtime guards remain as a
  belt-and-suspenders second line (a tagged test still refuses to run destructively
  without the env), but the build tag is now the primary boundary.
- **Needs Tim / coordinate:** backend-engineer adds the `//go:build integration` line
  to the 11 files and confirms `go test ./...` is green with **no** `DATABASE_URL`;
  release-engineer swaps the `contract` job for the `integration` job in `ci.yml` and
  updates the required-checks note (ADR-0014 §8) accordingly.

## Alternatives considered

- **Package-list split** (`go test $(go list ./... | grep -v /internal/store)` for
  unit, full list for integration) — rejected: simpler to bolt on but not grep-able,
  not opt-in per file, and it excludes the *whole* `internal/store` package from unit
  even though `quota_unit_test.go` there is a legitimate DB-free unit test. Build tags
  are per-file and explicit, and match the exact command the reference names.
- **Runtime-skip only (status quo)** — rejected: keeps compiling DB tests into the
  unit binary and keeps a Postgres service spinning in CI just so those tests don't
  skip; the unit run is neither hermetic nor honestly fast, and "unit" vs
  "integration" is invisible in the source tree.
- **A `//go:build unit` tag instead** — rejected: inverts the default so a forgotten
  tag silently drops a test from *both* runs; tagging the DB-dependent minority and
  leaving the pure majority as the untagged default fails safe (an untagged test
  always runs).

See ADR-0014 (the `contract` job this supersedes, and the CI/infra trust boundary),
ADR-0016 (the deployed-env black-box contract CI can't run), and ADR-0023 (the merged
coverage profile built from these two runs).
