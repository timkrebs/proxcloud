"use client";
// Wizard data sources — every dropdown is fed by the real cluster, through the
// tenant catalog projection (Phase 3): /api/tenants/{tenantId}/catalog/*.
// The catalog strips capacity/sensitive fields (Contributor+), so node lists
// are names only and storages carry id + content types (no free/total).
import { useQuery } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api/client";
import type { Bridge, CatalogNode, CatalogStorage, NextID, StorageContentItem } from "@/lib/api/generated/types";
import { useActiveTenantId } from "@/lib/stores/uiStore";

export function useCatalogNodes() {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: ["catalog", tenantId, "nodes"],
    queryFn: () => apiFetch<CatalogNode[]>(`/api/tenants/${tenantId}/catalog/nodes`),
    enabled: tenantId !== null,
  });
}

export function useNextId() {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: ["catalog", tenantId, "nextid"],
    queryFn: () => apiFetch<NextID>(`/api/tenants/${tenantId}/catalog/nextid`),
    staleTime: 0,
    enabled: tenantId !== null,
  });
}

export function useBridges(node: string) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: ["catalog", tenantId, node, "bridges"],
    queryFn: () =>
      apiFetch<Bridge[]>(`/api/tenants/${tenantId}/catalog/nodes/${encodeURIComponent(node)}/bridges`),
    enabled: tenantId !== null && node !== "",
  });
}

export function useNodeStorages(node: string, content: string) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: ["catalog", tenantId, node, "storages", content],
    queryFn: () =>
      apiFetch<CatalogStorage[]>(
        `/api/tenants/${tenantId}/catalog/nodes/${encodeURIComponent(node)}/storages?content=${content}`,
      ),
    enabled: tenantId !== null && node !== "",
  });
}

export function useStorageContent(node: string, storage: string, content: string) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: ["catalog", tenantId, node, "content", storage, content],
    queryFn: () =>
      apiFetch<StorageContentItem[]>(
        `/api/tenants/${tenantId}/catalog/nodes/${encodeURIComponent(node)}/storages/${encodeURIComponent(storage)}/content?content=${content}`,
      ),
    enabled: tenantId !== null && node !== "" && storage !== "",
  });
}
