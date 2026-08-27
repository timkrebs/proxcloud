// Central query-key factory — every TanStack Query key in the app comes
// from here so SSE-driven invalidation stays consistent.

export interface ResourceFilters {
  /**
   * Tenant scope (Phase 3). Lives INSIDE the filter object so
   * qk.resources(f) = ["resources", f] keeps its leading segment — the
   * prefix invalidations in sse.ts / mutations.ts on ["resources"] still match
   * — while a tenant switch still produces a distinct cache entry.
   */
  tenantId?: string;
  type?: "qemu" | "lxc";
  projectId?: string;
  node?: string;
  search?: string;
}

export interface TaskFilters {
  tenantId?: string;
  running?: boolean;
  vmid?: number;
}

export interface ActivityFilters {
  /** "audit" (Proxcloud actions) | "task" (raw Proxmox operations). */
  source?: "audit" | "task";
  projectId?: string;
  outcome?: string;
}

export const qk = {
  me: ["me"] as const,
  sessions: ["sessions"] as const,
  health: ["health"] as const,
  version: ["version"] as const,
  cluster: ["cluster"] as const,
  nodes: ["nodes"] as const,
  node: (node: string) => ["node", node] as const,
  nodeMetrics: (node: string, timeframe: string) => ["node", node, "metrics", timeframe] as const,
  resources: (filters: ResourceFilters = {}) => ["resources", filters] as const,
  pools: ["pools"] as const,
  storage: ["storage"] as const,
  tasks: (filters: TaskFilters = {}) => ["tasks", filters] as const,
  task: (upid: string) => ["task", upid] as const,
  taskLog: (upid: string) => ["task", upid, "log"] as const,
  notifications: ["notifications"] as const,
  liveMetrics: ["liveMetrics"] as const,
  pricing: ["pricing"] as const,

  // ── Tenancy (Phase 3) ────────────────────────────────────────────────────
  projects: (tenantId?: string) => ["projects", tenantId] as const,
  project: (tenantId?: string, projectId?: string) => ["project", tenantId, projectId] as const,
  members: (tenantId?: string) => ["members", tenantId] as const,
  tenantSummary: (tenantId?: string) => ["tenantSummary", tenantId] as const,

  // ── Invitations (Phase 5, Owner-only) ────────────────────────────────────
  invitations: (tenantId?: string) => ["invitations", tenantId] as const,

  // ── Quotas + activity (Phase 4) ──────────────────────────────────────────
  // Tenant-scoped: leading segment + tenant id, matching the pattern above so a
  // tenant switch produces a distinct cache entry and mutations can prefix-
  // invalidate ["quota"] / ["projectQuota"] regardless of the id.
  quota: (tenantId?: string) => ["quota", tenantId] as const,
  projectQuota: (tenantId?: string, projectId?: string) =>
    ["projectQuota", tenantId, projectId] as const,
  adminTenantQuota: (tenantId?: string) => ["adminTenantQuota", tenantId] as const,
  activity: (tenantId?: string, filters: ActivityFilters = {}) =>
    ["activity", tenantId, filters] as const,

  // ── Auto-shutdown schedules (ADR-0019) ───────────────────────────────────
  // Leading "schedule" segment so SSE schedule_warning can prefix-invalidate
  // every entry; `id` is "{node}/{type}/{vmid}" for resource scope, the project
  // id for project scope.
  schedule: (scope: "resource" | "project", id: string, tenantId?: string) =>
    ["schedule", tenantId, scope, id] as const,

  // ── TTL / ephemeral resources (ADR-0020) ─────────────────────────────────
  // Leading "ttl" segment so SSE ttl_warning can prefix-invalidate every entry.
  // scope: "guest" (id = "{node}/{type}/{vmid}"), "policy" (id = project id),
  // "list" (id = project id, the expiring-soon view).
  ttl: (scope: "guest" | "policy" | "list", id: string, tenantId?: string) =>
    ["ttl", tenantId, scope, id] as const,
};
