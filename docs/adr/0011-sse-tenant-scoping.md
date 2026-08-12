# ADR-0011: SSE tenant scoping

Date: 2026-08-12 · Status: accepted

## Context

`GET /api/events` (ADR-0004's live layer) streams two kinds of frame to any
authenticated session: cluster-wide **node metrics** (node status + capacity)
and **task events** for every guest on the cluster. Before tenancy this was
fine — one admin saw everything. Once tenants exist it is a leak: a tenant user
would receive task events for VMIDs they do not own (a cross-tenant existence
leak, contradicting the "cross-tenant → 404, no existence leak" iron rule) and
raw node capacity (contradicting "infra visibility = platform-admin only",
ADR-0007 §4). The stream must enforce the same boundary the REST surface does.

## Decision

- **Per-connection filtering, keyed on the session's active tenant.** On SSE
  connect the server resolves the subscriber's identity and
  `session.active_tenant_id` (the same inputs the authz chain uses) and derives
  the set of VMIDs owned by that tenant (the tenant-filtered `resource_ownership`
  query, reused from the scoped `resources` path).
- **Task-event frames** are delivered to a connection only when the event's VMID
  is in that connection's owned-VMID set. Platform-admin connections bypass the
  filter (see ADR-0007 §0.4: an admin on the tenant surface is effective Owner).
- **Node-metrics frames** are delivered **only to platform-admin** subscribers.
  Tenant dashboards source their live data from per-guest RRD (the existing guest
  `metrics` endpoints), not the cluster node stream.
- **The filter is always server-derived from the session, never client-asserted.**
  On tenant switch the client reconnects the `EventSource` (`activeTenantId` is in
  the `useEvents` effect deps) so the server re-derives the filter from the updated
  `active_tenant_id`. A client cannot widen its own view by tampering.
- **Scope (minimum for Phase 3):** the owned-VMID set is computed per connection
  at connect and refreshed on the interval the metrics loop already polls. A full
  redesign of the metrics pipeline — a dedicated admin-only node stream vs.
  aggregated per-tenant usage frames — is **deferred**.

## Consequences

- No cross-tenant task-event leak and no infra-capacity leak to tenant users:
  the stream enforces the same boundary as the REST 404 rule.
- Tenant live metrics this phase are per-guest (RRD) rather than a cluster-node
  aggregate. A per-tenant aggregate **usage** frame fits naturally with Phase 4's
  quota usage bars and is deferred to there or later.
- Each SSE connection recomputes an owned-VMID set on the existing poll interval,
  reusing the tenant-indexed ownership query — a bounded, indexed cost.
- Switching tenants forces a cheap EventSource reconnect; this is the same seam
  the frontend already uses to re-scope its React Query cache.
