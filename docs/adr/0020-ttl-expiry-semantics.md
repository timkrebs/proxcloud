# ADR-0020: TTL / ephemeral-resource expiry semantics

Date: 2026-08-27 · Status: accepted

## Context

Guests created for a demo or a test run live forever unless someone remembers to
delete them. TTL makes a guest optionally **ephemeral**: at a chosen time it is
either stopped or destroyed. This is the highest-consequence feature in the wave
— a mis-fired `delete` irreversibly destroys real infrastructure — so its
semantics (what expires do, when we warn, how extend works, how a destroy is
recorded, and what happens when the guest vanishes out of band) must be pinned
down before code.

TTL rides the scheduler (ADR-0018): warn and expire are `jobs` handlers. It
reuses the ownership tombstone-on-destroy lifecycle already in the tree
(ADR-0010, commits `8917b4a`/`a8abe81`, `ResolveOwnershipForTask`, `ON CONFLICT`
revive, create→delete→create-same-VMID regression) — this ADR **does not**
redesign that; TTL-delete is just another destroy path that calls into it.

## Decision

- **`ttls` table** (migration `000005_job_scheduler`, schema conventions per
  `000001_init.up.sql`):
  - `id` UUID PK
  - `tenant_id` / `project_id` / `vmid` — **unique** (one TTL per guest),
    composite FK `(tenant_id, project_id) → projects(tenant_id, id)`
  - `expires_at` timestamptz
  - `action` CHECK (`stop`|`delete`)
  - `warned_24h` bool default false, `warned_1h` bool default false
  - `original_duration` — the TTL length as chosen, used to size an extend.
    Stored as `bigint` **seconds** (not a pg `interval`) for a clean
    `int64 ↔ time.Duration` mapping in Go; likewise `project_ttl_policy`'s
    `default_ttl`/`max_ttl`.
  - `created_by` UUID FK `users(id)`, `created_at` / `updated_at`.

- **Project TTL policy lives on a `project_ttl_policy` table**, not on
  `projects`: `(tenant_id, project_id)` PK/FK, `default_ttl` interval nullable,
  `max_ttl` interval default `30 days`. Rationale: keep the hot `projects` row
  lean, allow the policy to grow (future: allowed actions, forced-TTL) without
  churning the core aggregate, and match the sidecar pattern quotas already use.
  Locked policy (ADR): **default none, max 30 days** — a guest is permanent
  unless its creator opts into a TTL, and **no TTL (at create or via extend) may
  exceed the project `max_ttl`.**

- **Warnings at T-24h and T-1h.** Two `ttl.warn` jobs per TTL publish the
  tenant-scoped SSE `ttl_warning` frame (no PVE UPID) carrying the VMID + expiry,
  from which the UI builds the **extend** action. **As implemented**, delivery is
  scoped by owned-VMID (the whole owning tenant, plus platform-admin) via
  `events.deliver` — the frame is registered as an explicit case so it never
  falls through to the cross-tenant broadcast default; it is NOT narrowed to the
  creator (no `created_by` filter), matching auto-shutdown's `schedule_warning`.
  The warning is deliberately NOT written to the process-global notification ring,
  which is unscoped and would leak one tenant's activity. `warned_24h`/`warned_1h`
  guard against at-least-once double-sends. A missed warning is `run_late`
  (ADR-0018): still worth sending late.

- **Extend is authenticated and capped.** The link resolves to a tenant-scoped
  POST `…/ttl/extend` (no unauthenticated token endpoint). Extend sets
  `expires_at += original_duration`, **capped at now + project `max_ttl`**,
  resets `warned_24h`/`warned_1h` to false, and **reschedules** the warn + expire
  `jobs`. Audited as system, action `ttl.extend`.

- **Expiry action `stop`.** The `ttl.expire` handler does graceful
  `GuestAction(shutdown)` → `Registry.Track` the UPID → `AwaitCompletion` up to
  the grace window → force `GuestAction(stop)` if still running (the
  graceful→force ladder, ADR-0018). The guest is **marked expired** (a distinct
  state from user-stop and from auto-shutdown-stop), surfaced as an "expired"
  badge and an "expiring soon / expired" project view. The guest and its config
  survive — expiry with `stop` is reversible by the user. Audited
  `guest.ttl.stop`, `actor_system='system:scheduler'`.

- **Expiry action `delete`.** The handler performs a **real Proxmox destroy**
  (`DeleteGuest`, requires the guest stopped — so graceful→force first), then
  **releases/tombstones `resource_ownership`** via the existing
  tombstone-on-destroy path (ADR-0010 — not reimplemented here), and writes a
  **tombstone audit entry whose `detail` jsonb carries a full config snapshot**
  of the destroyed guest (so an operator can reconstruct what was lost).
  Audited `guest.ttl.delete`, `actor_system='system:scheduler'`. Because this is
  irreversible, **selecting `delete` at creation requires typed confirmation**
  (reuse the existing delete-confirm pattern); `stop` does not.

- **Cancellation on out-of-band deletion.** If the guest is destroyed by anyone
  else (a user, a smoke test, another TTL) before its TTL fires, the TTL's warn
  and expire jobs must not act on a gone VMID. Two guards, both already in the
  ADR-0018 design: (1) every handler **re-reads `resource_ownership` by VMID**
  at execution and self-cancels its owner's jobs if the row is gone/tombstoned;
  (2) every destroy choke-point calls `CancelJobsForVMID` inline. Extending
  resets flags and reschedules; deleting the guest cancels.

- **Wizard + blade UX.** The create wizard offers `24h / 48h / 7d / 30d / custom
  / none`, defaulting to the project `default_ttl` and rejecting any choice over
  `max_ttl` (both read from an extended project-config payload). TTL is also
  settable/editable later from a TTL editor on the resource blade (sibling of the
  schedule blade, ADR-0019). Countdown chip + "expired" badge on the resource
  list and the blade Essentials row; loading/empty/error on every new view.

- **Roles.** Setting/extending/clearing a TTL requires **Contributor+**; Reader
  views only. Routes (`…/guests/{node}/{type}/{vmid}/ttl`, `…/ttl/extend`,
  `…/projects/{projectId}/ttl-policy`) are resource/project-scoped so they
  inherit `ResolveScope` ownership + 404-not-403 IDOR; each gets a
  `permissions.go` **and** `audit_actions.go` entry (completeness-tested).

## Consequences

- The single most dangerous action in the product (`delete`) is gated three
  ways: opt-in only, typed confirmation at creation, and two escalating warnings
  with a one-click extend before it fires — and it leaves a tombstone audit row
  with a full config snapshot, so a destroy is always accountable and
  post-mortem-reconstructable.
- `max_ttl` is a hard ceiling enforced on both create and extend, so no guest can
  be kept alive indefinitely by repeated extends past the project's policy.
- Reusing the existing tombstone-on-destroy path means TTL-delete and
  user-delete converge on one ownership-lifecycle code path; the
  create→delete→create-same-VMID regression already covers the revive case.
- `stop`-expiry is deliberately reversible (guest + config retained, just marked
  expired) so the low-stakes default is safe; only `delete` is destructive.
- One TTL per guest (`unique`) keeps warning-flag and extend bookkeeping
  unambiguous; a guest cannot have two competing expiries.
- A guest deleted out of band never leaves orphaned warn/expire jobs, via the
  defensive re-read + choke-point cancellation — no notification about, or
  destroy attempt on, a VMID that is already gone.

## Alternatives considered

- **TTL policy columns on `projects`.** Simpler join but bloats the hot core row
  and couples policy evolution to the aggregate; a `project_ttl_policy` sidecar
  (mirroring quotas) keeps `projects` lean and the policy independently
  extensible.
- **Unauthenticated one-click extend/skip token links** (signed URL in the
  email/notification). Rejected outright: an unauthenticated endpoint that
  mutates a tenant resource violates the authz/IDOR spine; the link resolves to
  an authenticated, tenant-scoped POST instead.
- **`delete` with no snapshot / no typed confirmation.** Rejected: an
  irreversible destroy with no pre-warning ceremony and no post-hoc record is
  incompatible with "honest task states" and the audit iron rule.
- **Extend by a fixed increment (e.g. +24h) instead of `original_duration`.**
  Less predictable for the user ("I made it a 7-day guest, extend should give me
  another 7 days"); `original_duration` capped at `max_ttl` is the intuitive
  behavior.
- **A dedicated PVE-gone drift detector to cancel TTL jobs.** Deferred (ADR-0018);
  the defensive-handler re-read plus destroy-choke-point cancellation covers the
  cases that matter without a new reconciler pass.
