"use client";
// Typed data hooks over the backend REST API. Polling intervals are the
// pre-SSE safety net; the SSE hook (lib/sse.ts) additionally invalidates
// these queries the moment something changes.
//
// Phase 3 surface split:
//  - Guest / resource / task calls are tenant-scoped: /api/tenants/{tenantId}/…
//    and wait for an active tenant before firing.
//  - Cluster-wide infrastructure (cluster, nodes, node metrics) is
//    platform-admin only: /api/admin/… gated behind me.isPlatformAdmin.
import { useQuery } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api/client";
import type {
  ClusterSummary,
  Pricing,
  GuestSummary,
  Health,
  Me,
  MetricsResponse,
  NodeSummary,
  Notification,
  TaskSummary,
  VersionInfo,
} from "@/lib/api/generated/types";
import { qk, type ResourceFilters } from "@/lib/api/queryKeys";
import { useActiveTenantId } from "@/lib/stores/uiStore";

export function useMe() {
  return useQuery({ queryKey: qk.me, queryFn: () => apiFetch<Me>("/api/auth/me"), staleTime: 5 * 60_000 });
}

/** True only once /api/auth/me has loaded and the caller is a platform admin. */
function useIsPlatformAdmin(): boolean {
  return !!useMe().data?.isPlatformAdmin;
}

export function useHealth() {
  return useQuery({
    queryKey: qk.health,
    queryFn: () => apiFetch<Health>("/api/health"),
    refetchInterval: 30_000,
  });
}

/**
 * Running binary's build metadata (public, no session). The values are baked in
 * at link time, so they never change while the tab is open — cache forever.
 */
export function useVersion() {
  return useQuery({
    queryKey: qk.version,
    queryFn: () => apiFetch<VersionInfo>("/api/v1/version"),
    staleTime: Infinity,
    gcTime: Infinity,
  });
}

// ── Admin infrastructure (platform-admin only) ───────────────────────────────

export function useCluster() {
  const enabled = useIsPlatformAdmin();
  return useQuery({
    queryKey: qk.cluster,
    queryFn: () => apiFetch<ClusterSummary>("/api/admin/cluster"),
    refetchInterval: 10_000,
    enabled,
  });
}

export function useNodes() {
  const enabled = useIsPlatformAdmin();
  return useQuery({
    queryKey: qk.nodes,
    queryFn: () => apiFetch<NodeSummary[]>("/api/admin/nodes"),
    refetchInterval: 10_000,
    enabled,
  });
}

export function useNodeMetrics(node: string, timeframe = "hour") {
  const enabled = useIsPlatformAdmin();
  return useQuery({
    queryKey: qk.nodeMetrics(node, timeframe),
    queryFn: () =>
      apiFetch<MetricsResponse>(`/api/admin/nodes/${encodeURIComponent(node)}/metrics?timeframe=${timeframe}`),
    refetchInterval: 60_000,
    enabled: enabled && node !== "",
  });
}

// ── Tenant-scoped resources + tasks ──────────────────────────────────────────

function resourceQuery(filters: ResourceFilters): string {
  const p = new URLSearchParams();
  if (filters.type) p.set("type", filters.type);
  if (filters.projectId) p.set("projectId", filters.projectId);
  if (filters.node) p.set("node", filters.node);
  if (filters.search) p.set("search", filters.search);
  const s = p.toString();
  return s ? `?${s}` : "";
}

export function useResources(filters: ResourceFilters = {}) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.resources({ ...filters, tenantId: tenantId ?? undefined }),
    queryFn: () => apiFetch<GuestSummary[]>(`/api/tenants/${tenantId}/resources${resourceQuery(filters)}`),
    refetchInterval: 10_000,
    enabled: tenantId !== null,
  });
}

export function useTasks(filters: { running?: boolean; vmid?: number } = {}) {
  const tenantId = useActiveTenantId();
  const p = new URLSearchParams();
  if (filters.running) p.set("running", "true");
  if (filters.vmid) p.set("vmid", String(filters.vmid));
  const s = p.toString();
  return useQuery({
    queryKey: qk.tasks({ ...filters, tenantId: tenantId ?? undefined }),
    queryFn: () => apiFetch<TaskSummary[]>(`/api/tenants/${tenantId}/tasks${s ? `?${s}` : ""}`),
    refetchInterval: 10_000,
    enabled: tenantId !== null,
  });
}

export function usePricing() {
  return useQuery({
    queryKey: qk.pricing,
    queryFn: () => apiFetch<Pricing>("/api/pricing"),
    staleTime: Infinity,
  });
}

export function useNotifications() {
  return useQuery({
    queryKey: qk.notifications,
    queryFn: () => apiFetch<Notification[]>("/api/notifications"),
    refetchInterval: 15_000,
  });
}
