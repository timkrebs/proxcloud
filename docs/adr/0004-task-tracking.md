# ADR-0004: Hybrid task tracking

Date: 2026-08-11 · Status: accepted

## Context

The UI needs transitional guest statuses (starting/stopping/…), durable
notifications, and a truthful activity log — but the backend should stay
stateless enough that a restart never lies about task history.

## Decision

- The **activity log proxies `/cluster/tasks` verbatim** (friendly labels
  mapped from PVE task types); Proxmox stays the source of truth.
- An **in-memory registry** tracks only Proxcloud-initiated tasks: the
  transitional status overlay on resource lists, the notification ring
  (cap 200), and deployment step progress. A 2 s watcher polls only
  running tracked tasks (typically 0–3) and publishes SSE events.
- No automatic retries on reads or mutations: pollers self-heal next
  tick; mutations must never double-fire.

## Consequences

A backend restart loses notification history and friendly labels for
in-flight tasks — never task truth. Deployment progress pages answer 404
after a restart with a pointer to the activity log.
