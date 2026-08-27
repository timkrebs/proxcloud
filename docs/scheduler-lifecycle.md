# Scheduler & Lifecycle

Proxcloud can act on guests on a schedule and at a chosen expiry, via a
persistent, tenant-aware job engine. Three layered features, each behind an env
flag and **off by default**:

1. **Job scheduler** (ADR-0018) — the engine. A Postgres `jobs` table claimed
   with `SELECT … FOR UPDATE SKIP LOCKED` so a second backend instance never
   double-fires; retry→backoff→dead-letter; timezone-aware cron; at-least-once
   delivery with idempotent, defensive handlers.
2. **Auto-shutdown schedules** (ADR-0019) — recurring, timezone-aware power-down
   (and optional auto-start) at resource or project level.
3. **TTL / ephemeral resources** (ADR-0020) — optional per-guest expiry that
   **stops** or **deletes** the guest, with advance warnings and one-click extend.

## Enabling

All flags default `false`. Auto-shutdown and TTL each also require the scheduler.

```bash
SCHEDULER_ENABLED=true          # the engine (required by the two features)
AUTOSHUTDOWN_ENABLED=true       # auto-shutdown schedules
TTL_ENABLED=true                # TTL / ephemeral resources
SCHEDULER_INTERVAL=30s          # claim-tick period (default 30s)
AUTOSHUTDOWN_DEFAULT_GRACE=120s # graceful→force-stop grace (per-schedule override allowed)
```

When a flag is off, its routes still persist definitions but **materialize no
jobs**, and its scheduler handlers are not registered — the feature is inert, not
half-on. A guest's timezone-aware schedule survives DST because the tz database is
embedded in the binary (`time/tzdata`).

## Auto-shutdown

A schedule is **structured** (never raw cron): a `HH:MM` shutdown time, optional
auto-start time, days-of-week, an IANA timezone, and a force-stop grace. The
backend derives the cron internally. Precedence: a **resource** schedule wins
over the **project** schedule; a resource row with `opt_out` exempts the guest.

- **Stop**: graceful ACPI shutdown, force-stop after the grace window
  (PVE-delegated `timeout`+`forceStop`, status-verified). The guest is marked
  `auto_stopped` — distinct from a user-stop, so the paired **auto-start** only
  powers back on a guest the scheduler stopped. A user start clears the marker.
- **Warning**: a heads-up fires 15 min before shutdown over the tenant-scoped SSE
  `schedule_warning` frame.
- **Skip next**: advances the next occurrence by one cron boundary without
  disabling the schedule.

Roles: Contributor+ manages; Reader views. Edit on the resource blade's
**Schedule** tab or the project panel.

## TTL / ephemeral resources

A guest can be made ephemeral: at `expires_at` it is **stopped** (reversible —
powered off + marked `expired`, cleared by a user start) or **deleted** (a real
Proxmox `purge` destroy). One TTL per guest.

- **Warnings** at **T-24h** and **T-1h** over the tenant-scoped SSE `ttl_warning`
  frame; each is sent once (`warned_24h`/`warned_1h`).
- **Extend** adds one original TTL duration, **capped at the project maximum** —
  repeated extends can never exceed the ceiling. Extending resets the warnings
  and reschedules.
- **Project policy** (`project_ttl_policy`, Owner-managed): a `default_ttl` (none
  by default) and a hard `max_ttl` (**30 days** default). No TTL, at set or
  extend, may exceed `max_ttl`.

### Delete safety

`delete` is the only irreversible action in the product; it is gated three ways:

1. **Opt-in** — a guest is permanent unless a TTL is set.
2. **Typed confirmation** — arming a `delete` TTL requires typing the guest's
   name, enforced server-side.
3. **Two escalating warnings** with one-click extend before it fires.

At expiry the destroy runs only after the guest is confirmed stopped, and
ownership is released + audited only after a **confirmed-successful** destroy. The
tombstone audit row carries a **full config snapshot** in its `detail` so an
operator can reconstruct what was destroyed. (The snapshot is the config, not a
disk backup — a purge destroy remains irreversible.)

## How it stays honest & isolated

- Every scheduler-initiated mutation is **audited as system**
  (`actor_system="system:scheduler"`, e.g. `guest.scheduler.stop`,
  `guest.ttl.delete`), fail-closed (intent before mutation). User actions
  (set/extend/clear) are audited as the real user through the normal middleware.
- Every warning is delivered **only** over its VMID-scoped SSE frame (owning
  tenant + platform-admin) — never through the process-global notification ring.
- Handlers are **defensive**: each re-reads ownership at execution and cancels
  its own jobs if the guest is gone/tombstoned; a destroy choke-point cancels a
  gone guest's jobs — no orphaned job ever acts on a reused VMID.
- All routes are tenant-scoped and inherit the 404-not-403 ownership check; each
  has a permission-table + audit-action entry (completeness-tested).

## API surface (all under `/api/tenants/{tenantId}`)

```
# Auto-shutdown
GET|PUT|DELETE  /guests/{node}/{type}/{vmid}/schedule
POST            /guests/{node}/{type}/{vmid}/schedule/skip
GET|PUT|DELETE  /projects/{projectId}/schedule

# TTL
GET|PUT|DELETE  /guests/{node}/{type}/{vmid}/ttl        # PUT delete-action needs confirmName
POST            /guests/{node}/{type}/{vmid}/ttl/extend
GET|PUT         /projects/{projectId}/ttl-policy         # PUT is Owner-only
GET             /projects/{projectId}/ttls               # expiring-soon / expired view
```

See `docs/adr/0018`–`0020` for the design decisions and `docs/proxmox/lifecycle.md`
for the PVE stop/start/destroy semantics the handlers rely on.

## Not yet implemented

Setting a TTL **at guest creation** (the wizard field applying the project
`default_ttl`) is a planned follow-up; today a TTL is set/edited after creation
from the guest's **Lifecycle** blade.
