# ADR-0019: Auto-shutdown schedule data model

Date: 2026-08-27 · Status: accepted

## Context

Auto-shutdown lets a user power a guest (or every guest in a project) down on a
recurring, timezone-aware calendar, with optional auto-start, so a homelab can
sleep overnight. It rides the scheduler engine (ADR-0018): each schedule
materializes into `jobs` rows the worker fires.

The naive design — let the user type a cron string — is wrong for this product.
Cron is error-prone, has no first-class timezone/DST handling for a
non-technical operator, cannot be validated in a friendly editor, and cannot be
rendered as a human-readable "every weekday at 22:00 Europe/Berlin" without
round-tripping. The design-fidelity rule wants a structured editor with a
`TimezonePicker`, not a text box. We also need clear inheritance semantics
because a schedule can live at the **project** level (applies to all guests) or
the **resource** level (one guest), and the two must compose predictably.

## Decision

- **`schedules` table** (migration `000005_job_scheduler`, schema conventions
  per `000001_init.up.sql`):
  - `id` UUID PK
  - `scope` CHECK (`resource`|`project`)
  - `tenant_id` / `project_id` / `vmid` (VMID nullable — NULL for `project`
    scope), composite FK `(tenant_id, project_id) → projects(tenant_id, id)`
  - `shutdown_time` text `HH:MM` (24h, local to `timezone`)
  - `auto_start_time` text `HH:MM` nullable — optional power-on
  - `days_of_week` — `int[]` (0–6, Sun–Sat) which days the schedule fires
  - `timezone` text — IANA name (e.g. `Europe/Berlin`), validated against the
    tz database
  - `grace_seconds` int default `120` — per-schedule override of the global
    force-stop grace (ADR-0018 lock: default 120s)
  - `enabled` bool default true
  - `opt_out` bool default false — a resource row that suppresses an inherited
    project schedule
  - `created_by` UUID FK `users(id)`, `created_at` / `updated_at`
  - unique `(tenant_id, vmid)` for resource rows; one project schedule per
    `(tenant_id, project_id)`.

- **Cron is derived internally, never user-entered.** The backend composes a
  cron spec from `shutdown_time` + `days_of_week` + `timezone` and stores it on
  the emitted `jobs` row (`cron`, `timezone`); `Schedule.Next(t)` from
  `robfig/cron/v3` (ADR-0018) computes the next fire in the schedule's IANA
  timezone, so DST transitions are handled by the tz database rather than by us.
  `auto_start_time`, when set, emits a **separate** `autoshutdown.start` job.
  The `schedules` table is the source of truth; `jobs` rows are its projection
  and are recomputed on any edit.

- **Resource overrides project; opt-out suppresses.** Resolution for a given
  guest:
  1. a `resource`-scope row for its VMID, if present, **wins outright**;
  2. else the `project`-scope row applies to the guest;
  3. a `resource` row with `opt_out=true` means "this guest is exempt from the
     project schedule" and emits **no** jobs.
  Inheritance is resolved at schedule-edit time (jobs re-materialized), not at
  fire time, so the worker never has to reason about precedence.

- **Skip next.** A one-click POST `…/schedule/skip` marks the single next
  occurrence exempt — a per-occurrence `skip` (ADR-0018 missed-policy
  vocabulary) applied to the next `jobs` row (a `skip_until` / flag): the next
  tick skips that fire and reschedules the one after. It does not disable the
  schedule.

- **T-15m warning.** A companion `autoshutdown.warn` job at fire-minus-15-min
  calls the new standalone notify path `Registry.Notify(vmid, …)` (not tied to a
  PVE UPID) carrying the owning VMID, the creator's user id, and a skip link;
  `events.deliver` filters the frame by owned-VMID and, for warnings, prefers
  `created_by`, and the new frame name is added explicitly so it does not fall
  through the default cross-tenant broadcast case.

- **Auto-shutdown-stopped ≠ user-stopped.** The `autoshutdown.stop` handler
  marks the stop as scheduler-originated (a marker in the ownership/notification/
  audit `detail`), so (a) the UI shows "stopped by schedule" distinctly, and
  (b) the paired `autoshutdown.start` job only powers a guest back on if it was
  scheduler-stopped — a guest a user deliberately stopped is not auto-started.
  The stop is audited as system (`actor_system='system:scheduler'`, action
  `guest.scheduler.stop`), per ADR-0018.

- **Roles.** Creating/editing/skipping/deleting a schedule requires
  **Contributor+**; **Reader** can view but not manage. Routes are
  resource-scoped (`…/guests/{node}/{type}/{vmid}/schedule`, `…/schedule/skip`)
  or project-scoped (`…/projects/{projectId}/schedule`) so they inherit
  `ResolveScope`'s ownership + 404-not-403 IDOR check and the audit-on-mutation
  middleware; each gets an entry in `internal/authz/permissions.go` **and**
  `internal/authz/audit_actions.go` (both completeness-tested).

## Consequences

- The schedule editor is a structured form (time + weekday chips +
  `TimezonePicker` over `Intl.supportedValuesOf('timeZone')`), matching the
  design; no user ever sees or types cron, and every schedule renders as plain
  English.
- DST correctness is inherited from the tz database via `robfig/cron/v3.Next`
  rather than owned by us; a 22:00 Europe/Berlin shutdown stays at local 22:00
  across the spring/autumn switch.
- Precedence is decided once, at edit time, by re-materializing `jobs`; the
  worker stays dumb and the "why did this guest not shut down" question has a
  single answerable source (the resolved schedule + its jobs).
- Distinguishing scheduler-stop from user-stop is load-bearing for auto-start
  correctness and for an honest activity log; it is a small marker but must be
  set on every scheduler stop.
- Project-scope schedules fan out to a job per guest; a large project produces
  many `jobs` rows. Acceptable at homelab scale, and the partial index on
  `status='scheduled'` keeps the claim query cheap.

## Alternatives considered

- **User-entered cron string.** Rejected: hostile to non-technical operators,
  no friendly validation, no clean timezone story, not renderable, and a design
  mismatch. Structured fields with internally-derived cron give us both a good
  editor and a correct scheduler.
- **Store the resolved schedule only on `jobs`, no `schedules` table.** Loses
  the durable, editable definition (inheritance, opt-out, the human view) and
  couples the edit UX to the job projection; the definition/projection split is
  cleaner and mirrors how TTL (ADR-0020) separates state from jobs.
- **A single boolean "exempt" instead of the override-vs-opt-out pair.**
  Cannot express "this guest keeps its own schedule *and* ignores the project
  one" separately from "this guest is exempt entirely"; the explicit
  `scope=resource` row (override) plus `opt_out` flag (suppress) covers both.
- **Fire-time precedence resolution.** Would push inheritance logic into the hot
  claim path and make double-fires (at-least-once) reason about precedence;
  resolving at edit time keeps the worker trivial.
