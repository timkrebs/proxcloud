"use client";
// Wizard data sources — every dropdown is fed by the real cluster.
import { useQuery } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api/client";
import type { Bridge, NextID, NodeStorage, StorageContentItem } from "@/lib/api/generated/types";

export function useNextId() {
  return useQuery({
    queryKey: ["nextid"],
    queryFn: () => apiFetch<NextID>("/api/cluster/nextid"),
    staleTime: 0,
  });
}

export function useBridges(node: string) {
  return useQuery({
    queryKey: ["catalog", node, "bridges"],
    queryFn: () => apiFetch<Bridge[]>(`/api/nodes/${encodeURIComponent(node)}/bridges`),
    enabled: node !== "",
  });
}

export function useNodeStorages(node: string, content: string) {
  return useQuery({
    queryKey: ["catalog", node, "storages", content],
    queryFn: () =>
      apiFetch<NodeStorage[]>(`/api/nodes/${encodeURIComponent(node)}/storages?content=${content}`),
    enabled: node !== "",
  });
}

export function useStorageContent(node: string, storage: string, content: string) {
  return useQuery({
    queryKey: ["catalog", node, "content", storage, content],
    queryFn: () =>
      apiFetch<StorageContentItem[]>(
        `/api/nodes/${encodeURIComponent(node)}/storages/${encodeURIComponent(storage)}/content?content=${content}`,
      ),
    enabled: node !== "" && storage !== "",
  });
}
