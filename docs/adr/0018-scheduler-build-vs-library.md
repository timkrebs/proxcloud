# ADR-0018: Scheduler — build vs. library

Date: 2026-08-27 · Status: accepted

## Context

The Scheduler & Lifecycle wave (auto-shutdown, TTL expiry) needs a
**persistent, tenant-aware job engine** that survives restarts, is safe when a
second backend instance runs (blue/green deploy, ADR-0015), and fires every
scheduler-initiated mutation through the *same* authz + audit spine a user
mutation does. Nothing in the tree schedules future work today; every guest
state change is user-initiated and synchronous.

The codebase already owns every primitive such an engine needs:

- the worker template `backend/internal/reconciler/reconciler.go` —
  `Reconciler{Store, Log, Interval, TTL, Now func() time.Time}`, `Run(ctx)`
  (immediate sweep then `time.NewTicker` + `select` on `ctx.Done`, non-positive
  interval disables with a WARN), an exported `Sweep(ctx)` for deterministic
  tests, and `reclaim` (reconciler.go:106) — the fail-closed, audit-first,
  idempotent mutation template;
- the audit spine `backend/internal/auditz/auditz.go`
  (`Recorder.Begin → *Pending → Finalize`, intent-before/outcome-after);
- the store seam `backend/internal/store/store.go` — sub-interfaces, reentrant
  `WithTx(ctx, fn)`, `AdvisoryLock` (`pg_advisory_xact_lock`), `ErrNotFound`/
  `ErrConflict`;
- injectable `Now func() time.Time` clocks for deterministic time travel;
- the 404-not-403 IDOR authz path and the "every mutation audited" completeness
  tests (tenancy iron rules).

A general job library (river, machinery, asynq) would bring its **own**
migrations, its **own** worker/retry/audit model, and its **own** notion of a
"job owner" — none of which know about tenants, projects, `resource_ownership`
tombstones, the 404-IDOR rule, or the "acted as system:scheduler" audit
requirement. We would spend more effort bending the library to those invariants
than writing the ~1 table + 1 worker the reconciler already shows how to build.

## Decision

- **Hand-roll the scheduler** on the reconciler template. New
  `internal/scheduler/scheduler.go`: `Scheduler{Store, Log, Interval,
  Now func() time.Time}`, `Run(ctx)` (immediate `Tick` then ticker/select),
  exported `Tick(ctx)` for tests. Wired as a sibling worker in
  `cmd/proxcloud/main.go`, gated on `cfg.SchedulerEnabled`.

- **One new dependency, a cron *spec parser* only:** `github.com/robfig/cron/v3`
  for timezone-aware `Schedule.Next(t time.Time)`. We use its parser and
  `Next`; we do **not** use its `cron.Cron` runner, its in-memory scheduling, or
  any job-framework surface. Writing a correct RFC-compliant, DST-aware cron
  evaluator by hand is the one piece not worth re-deriving.

- **`jobs` table** (migration `000005_job_scheduler`, schema conventions per
  `000001_init.up.sql` — UUID PK `gen_random_uuid()`, CHECK-constraint enums,
  jsonb, composite FK `(tenant_id, project_id) → projects(tenant_id, id)`,
  partial indexes):
  - `kind` CHECK (`recurring`|`one_shot`)
  - `handler` text (dispatch key, e.g. `autoshutdown.stop`, `autoshutdown.start`,
    `autoshutdown.warn`, `ttl.expire`, `ttl.warn`)
  - `tenant_id` / `project_id` / `vmid` — owner (nullable for non-resource jobs)
  - `payload` jsonb
  - `cron` text nullable, `timezone` text nullable (IANA) — recurring only
  - `run_at` timestamptz — next fire (one-shot uses it directly)
  - `status` CHECK (`scheduled`|`running`|`failed`|`succeeded`|`cancelled`)
  - `attempts` int, `max_attempts` int, `last_error` text
  - `locked_at` timestamptz, `locked_by` text — the claim
  - `missed_policy` CHECK (`catch_up`|`skip`|`run_late`)
  - `created_at` / `updated_at`
  - partial index `(run_at) WHERE status='scheduled'`; index `(vmid)` and
    `(tenant_id, status)` for owner-cancel + the admin view.

- **Claim protocol — `SELECT … FOR UPDATE SKIP LOCKED`.** `ClaimDueJobs(ctx,
  now, limit)` runs inside `WithTx`:
  `SELECT … WHERE status='scheduled' AND run_at <= now ORDER BY run_at
  FOR UPDATE SKIP LOCKED LIMIT n`, sets `status='running'`, `locked_at=now`,
  `locked_by=<instance id>`, and commits. A second instance ticking
  concurrently skips the already-locked rows rather than blocking or
  re-selecting them, so **no job double-fires**. The claim and the status flip
  are one transaction; a crash mid-handler leaves the row `running` and it is
  re-claimed after a lock-expiry threshold (a `running`+stale-`locked_at`
  sweep), giving **at-least-once** delivery.

- **Handlers are idempotent and defensive.** At-least-once means a handler may
  run twice for one logical fire, so each handler is written to no-op safely on
  repeat (an already-stopped guest is a logged no-op, not an error). Every
  handler **re-reads `resource_ownership` by VMID at execution time**; if the
  row is gone or tombstoned (guest destroyed out of band, by a user, or by a
  TTL-delete) it **cancels its own remaining jobs for that owner**
  (`CancelJobsForVMID`) and exits cleanly — no orphaned job ever acts on a gone
  VMID. This is the tree's substitute for a DB-present/PVE-gone drift detector
  (deferred), and it composes with the ownership tombstone-on-destroy fix
  already in the tree (ADR-0010, commits `8917b4a`/`a8abe81`).

- **Retry → backoff → dead-letter.** On handler error, `FailJob` increments
  `attempts`, records `last_error`, and if `attempts < max_attempts` reschedules
  `run_at` with exponential backoff (`status='scheduled'`); once
  `attempts >= max_attempts` the row is dead-lettered to `status='failed'`
  (visible in the admin jobs view) and stops retrying.

- **Missed-window policy, per `jobs.missed_policy`.** When the worker was down
  or overloaded and `run_at` is in the past at claim time:
  - `catch_up` — recurring jobs whose next-tick supersedes the miss run **once**
    now and reschedule to the next boundary (a missed auto-shutdown powers down
    at next tick, it does not fire the backlog N times);
  - `run_late` — the action still matters even late: a missed **TTL expiry**
    still executes, a missed TTL/auto-shutdown warning is sent late;
  - `skip` — the missed occurrence is abandoned and only future occurrences run
    (the "skip next" primitive is a per-occurrence `skip`).

- **Audit as system.** `audit_log.actor_user_id` is a nullable UUID FK to
  `users(id)` (000001_init.up.sql:~154) — a literal `system:scheduler` string
  cannot be stored there. Following the reconciler precedent
  (`ActorUserID: nil`), scheduler actions write `actor_user_id = NULL` plus:
  - a **dedicated action namespace** — `guest.scheduler.stop`,
    `guest.scheduler.start`, `guest.ttl.stop`, `guest.ttl.delete`,
    `schedule.skip`, `ttl.extend`;
  - a **new nullable column `actor_system text`** on `audit_log` (added in
    000005) set to `system:scheduler`, so the activity log renders the actor
    honestly;
  - the owning `schedule_id` / `ttl_id` / `job_id` carried in the existing
    `detail` jsonb.
  Every scheduler mutation goes through `auditz.Recorder.Begin/Finalize`
  (intent-before, outcome-after), exactly as `reclaim` does.

- **Graceful drain.** `main.go` starts background workers with no join today.
  Add a `sync.WaitGroup`: on shutdown `bgCancel()` then a bounded `wg.Wait()`,
  so an in-flight at-least-once handler finishes (or its row is left `running`
  to be re-claimed) rather than being torn down mid-action.

## Consequences

- One small migration and one worker file reuse machinery the team already
  understands; there is **no scheduler library in `go.mod`** and no parallel
  worker/audit/ownership model to keep in sync with ours.
- Tenant-ownership, the 404-IDOR authz path, and the audit spine apply to
  scheduler-initiated mutations for free, because they *are* the same store,
  authz, and audit code the request path uses.
- At-least-once + idempotent-and-defensive handlers is a deliberate trade: we
  accept the occasional double-run (harmless by construction) to avoid the
  exactly-once distributed-consensus complexity a library would also only
  approximate.
- Dead-lettered jobs sit in `status='failed'` for an operator to inspect; there
  is no auto-purge — consistent with "drift is visible, not destructive"
  (ADR-0010).
- We take on maintenance of the claim/backoff/missed-policy logic ourselves; it
  is small, table-driven-testable (`Tick` + injectable `Now`), and covered by an
  explicit no-double-fire SKIP LOCKED concurrency test.

## Alternatives considered

- **`riverqueue/river`** (Postgres-backed Go job queue). Mature and idiomatic,
  but it brings its own migrations and worker/leadership/retry model, and its
  job record has no place for our tenant/project/VMID ownership, the
  404-not-403 rule, or the `actor_system` audit contract. We would wrap every
  river job in an adapter that re-implements ownership re-read, tenant-scoped
  cancellation, and audit-as-system — i.e. write our engine anyway, on top of a
  dependency whose scheduling half we would not use. The cost of adopting it
  exceeds the cost of the `jobs` table.
- **`robfig/cron` full runner / `asynq` / `machinery`:** in-memory or
  Redis-centric, not a persistent Postgres system-of-record; a restart loses
  state or adds Redis to the stack. We already have Postgres and `WithTx`.
- **A `pg_cron` / database-side scheduler:** moves business logic (ownership
  re-read, PVE calls, audit) out of Go and into SQL/extensions, unavailable and
  untestable with the existing mocked store; rejected.
