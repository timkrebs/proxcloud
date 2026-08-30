"use client";
// Service catalog data layer (ADR-0026). Reads are Reader; provisioning is
// Contributor. The catalog is global (every tenant sees the same set) but the
// routes are tenant-scoped so they inherit the tenant's authz + audit spine.
//
// When CATALOG_ENABLED is off the endpoints return 404 (no capability leak).
// The gallery treats that 404 as "no catalog services" rather than an error —
// see isCatalogDisabled below.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { ApiError, apiFetch } from "@/lib/api/client";
import type {
  CatalogService,
  CatalogServiceList,
  ProvisionServiceRequest,
  ProvisionServiceResponse,
} from "@/lib/api/generated/types";
import { qk } from "@/lib/api/queryKeys";
import { useActiveTenantId } from "@/lib/stores/uiStore";

/**
 * True when a catalog query error is the "feature off" 404 rather than a real
 * failure. The gallery hides its catalog section on this signal instead of
 * showing an error card.
 */
export function isCatalogDisabled(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404;
}

/** Don't burn retries on a 4xx (a disabled catalog 404s deterministically). */
function noRetryOn4xx(failureCount: number, err: unknown): boolean {
  if (err instanceof ApiError && err.status >= 400 && err.status < 500) return false;
  return failureCount < 2;
}

/** The marketplace gallery (GET /service-catalog). */
export function useServiceCatalog() {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.serviceCatalog(tenantId ?? undefined),
    queryFn: async () => {
      const list = await apiFetch<CatalogServiceList>(`/api/tenants/${tenantId}/service-catalog`);
      return list.services;
    },
    enabled: tenantId !== null,
    staleTime: 5 * 60_000,
    retry: noRetryOn4xx,
  });
}

/** A single service definition (GET /service-catalog/{serviceId}) for prefill. */
export function useService(serviceId: string | null) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.service(tenantId ?? undefined, serviceId ?? undefined),
    queryFn: () =>
      apiFetch<CatalogService>(
        `/api/tenants/${tenantId}/service-catalog/${encodeURIComponent(serviceId!)}`,
      ),
    enabled: tenantId !== null && !!serviceId,
    staleTime: 5 * 60_000,
    retry: noRetryOn4xx,
  });
}

/**
 * Provision a catalog service (POST …/service-catalog/{serviceId}/provision).
 * The 202 response carries the one-time generated credential — the caller MUST
 * surface it exactly once (it is never stored, logged, or returned again) before
 * routing to the deployment page. Like useCreateGuest this hook has NO onError
 * toast so the wizard can route a 409 quota_exceeded to its inline sizing error.
 */
export function useProvisionService(serviceId: string) {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: (req: ProvisionServiceRequest) => {
      if (!tenantId) throw new Error("No active tenant selected");
      return apiFetch<ProvisionServiceResponse>(
        `/api/tenants/${tenantId}/service-catalog/${encodeURIComponent(serviceId)}/provision`,
        { method: "POST", body: JSON.stringify(req) },
      );
    },
    onSuccess: () => {
      // A new guest was reserved + submitted: refresh the resource list and the
      // quota bars (prefix keys hit every tenant/project entry).
      qc.invalidateQueries({ queryKey: ["resources"] });
      qc.invalidateQueries({ queryKey: ["quota"] });
      qc.invalidateQueries({ queryKey: ["projectQuota"] });
    },
  });
}
