// Central query-key factory — every TanStack Query key in the app comes
// from here so SSE-driven invalidation stays consistent.

export interface ResourceFilters {
  type?: "qemu" | "lxc";
  pool?: string;
  node?: string;
  search?: string;
}

export const qk = {
  me: ["me"] as const,
  health: ["health"] as const,
  cluster: ["cluster"] as const,
  nodes: ["nodes"] as const,
  node: (node: string) => ["node", node] as const,
  nodeMetrics: (node: string, timeframe: string) => ["node", node, "metrics", timeframe] as const,
  resources: (filters: ResourceFilters = {}) => ["resources", filters] as const,
  pools: ["pools"] as const,
  storage: ["storage"] as const,
  tasks: (filters: { running?: boolean; vmid?: number } = {}) => ["tasks", filters] as const,
  task: (upid: string) => ["task", upid] as const,
  taskLog: (upid: string) => ["task", upid, "log"] as const,
  notifications: ["notifications"] as const,
  liveMetrics: ["liveMetrics"] as const,
};
