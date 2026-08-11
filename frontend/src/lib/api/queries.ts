"use client";
// Typed data hooks over the backend REST API. Polling intervals are the
// pre-SSE safety net; the SSE hook (lib/sse.ts) additionally invalidates
// these queries the moment something changes.
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
  Pool,
  TaskSummary,
} from "@/lib/api/generated/types";
import { qk, type ResourceFilters } from "@/lib/api/queryKeys";

export function useMe() {
  return useQuery({ queryKey: qk.me, queryFn: () => apiFetch<Me>("/api/auth/me"), staleTime: 5 * 60_000 });
}

export function useHealth() {
  return useQuery({
    queryKey: qk.health,
    queryFn: () => apiFetch<Health>("/api/health"),
    refetchInterval: 30_000,
  });
}

export function useCluster() {
  return useQuery({
    queryKey: qk.cluster,
    queryFn: () => apiFetch<ClusterSummary>("/api/cluster"),
    refetchInterval: 10_000,
  });
}

export function useNodes() {
  return useQuery({
    queryKey: qk.nodes,
    queryFn: () => apiFetch<NodeSummary[]>("/api/nodes"),
    refetchInterval: 10_000,
  });
}

export function useNodeMetrics(node: string, timeframe = "hour") {
  return useQuery({
    queryKey: qk.nodeMetrics(node, timeframe),
    queryFn: () => apiFetch<MetricsResponse>(`/api/nodes/${encodeURIComponent(node)}/metrics?timeframe=${timeframe}`),
    refetchInterval: 60_000,
    enabled: node !== "",
  });
}

function resourceQuery(filters: ResourceFilters): string {
  const p = new URLSearchParams();
  if (filters.type) p.set("type", filters.type);
  if (filters.pool) p.set("pool", filters.pool);
  if (filters.node) p.set("node", filters.node);
  if (filters.search) p.set("search", filters.search);
  const s = p.toString();
  return s ? `?${s}` : "";
}

export function useResources(filters: ResourceFilters = {}) {
  return useQuery({
    queryKey: qk.resources(filters),
    queryFn: () => apiFetch<GuestSummary[]>(`/api/resources${resourceQuery(filters)}`),
    refetchInterval: 10_000,
  });
}

export function usePools() {
  return useQuery({ queryKey: qk.pools, queryFn: () => apiFetch<Pool[]>("/api/pools"), staleTime: 60_000 });
}

export function useTasks(filters: { running?: boolean; vmid?: number } = {}) {
  const p = new URLSearchParams();
  if (filters.running) p.set("running", "true");
  if (filters.vmid) p.set("vmid", String(filters.vmid));
  const s = p.toString();
  return useQuery({
    queryKey: qk.tasks(filters),
    queryFn: () => apiFetch<TaskSummary[]>(`/api/tasks${s ? `?${s}` : ""}`),
    refetchInterval: 10_000,
  });
}

export function usePricing() {
  return useQuery({
    queryKey: ["pricing"],
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
