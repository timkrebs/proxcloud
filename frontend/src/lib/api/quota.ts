"use client";
// Quota + activity data layer (Phase 4). Every read is tenant-prefixed and
// gates on a non-null active tenant (enabled); mutations invalidate the bars so
// usage refreshes immediately. The backend is authoritative — these hooks only
// surface its truth (limits/usage/remaining) and its errors verbatim.
import { useInfiniteQuery, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api/client";
import type {
  ActivityPage,
  ProjectQuotaResponse,
  QuotaWithUsage,
  SetQuotaRequest,
} from "@/lib/api/generated/types";
import { qk, type ActivityFilters } from "@/lib/api/queryKeys";
import { useMe } from "@/lib/api/queries";
import { useActiveTenantId } from "@/lib/stores/uiStore";
import { pushToast } from "@/lib/stores/toastStore";

const ACTIVITY_PAGE = 50;

// ── Tenant quota (Reader) ─────────────────────────────────────────────────────

/** Tenant-wide limits + live usage for the dashboard / directory usage bars. */
export function useTenantQuota() {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.quota(tenantId ?? undefined),
    queryFn: () => apiFetch<QuotaWithUsage>(`/api/tenants/${tenantId}/quota`),
    enabled: tenantId !== null,
    refetchInterval: 30_000,
  });
}

// ── Project quota (Reader) ────────────────────────────────────────────────────

/**
 * Project limits+usage AND the parent tenant rollup (ProjectQuotaResponse) so
 * callers can bind on the tighter remaining. Fires only once a project is
 * picked and a tenant is active.
 */
export function useProjectQuota(projectId: string | null | undefined) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.projectQuota(tenantId ?? undefined, projectId ?? undefined),
    queryFn: () =>
      apiFetch<ProjectQuotaResponse>(
        `/api/tenants/${tenantId}/projects/${encodeURIComponent(projectId!)}/quota`,
      ),
    enabled: tenantId !== null && !!projectId,
  });
}

// ── Project quota edit (Owner) ────────────────────────────────────────────────

export function usePutProjectQuota() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: ({ projectId, body }: { projectId: string; body: SetQuotaRequest }) =>
      apiFetch<QuotaWithUsage>(
        `/api/tenants/${tenantId}/projects/${encodeURIComponent(projectId)}/quota`,
        { method: "PUT", body: JSON.stringify(body) },
      ),
    onSuccess: (_res, { projectId }) => {
      qc.invalidateQueries({ queryKey: qk.projectQuota(tenantId ?? undefined, projectId) });
      qc.invalidateQueries({ queryKey: qk.quota(tenantId ?? undefined) });
      pushToast({ kind: "ok", title: "Quota updated", desc: "Project limits saved." });
    },
  });
}

// ── Tenant quota (platform-admin) ─────────────────────────────────────────────

/** Platform-admin read of a tenant's quota — prefills the tenant-quota editor. */
export function useAdminTenantQuota() {
  const tenantId = useActiveTenantId();
  const isAdmin = !!useMe().data?.isPlatformAdmin;
  return useQuery({
    queryKey: qk.adminTenantQuota(tenantId ?? undefined),
    queryFn: () => apiFetch<QuotaWithUsage>(`/api/admin/tenants/${tenantId}/quota`),
    enabled: tenantId !== null && isAdmin,
  });
}

export function usePutAdminTenantQuota() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: (body: SetQuotaRequest) =>
      apiFetch<QuotaWithUsage>(`/api/admin/tenants/${tenantId}/quota`, {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.adminTenantQuota(tenantId ?? undefined) });
      qc.invalidateQueries({ queryKey: qk.quota(tenantId ?? undefined) });
      // Tenant caps bound every project's remaining — refresh those bars too.
      qc.invalidateQueries({ queryKey: ["projectQuota"] });
      pushToast({ kind: "ok", title: "Quota updated", desc: "Tenant limits saved." });
    },
  });
}

// ── Activity log (Reader) ─────────────────────────────────────────────────────

/**
 * Keyset-paginated merged activity timeline. "Load more" advances the cursor by
 * passing the previous page's nextBefore back as ?before= (RFC3339); the query
 * ends when nextBefore is absent.
 */
export function useActivity(filters: ActivityFilters = {}) {
  const tenantId = useActiveTenantId();
  return useInfiniteQuery({
    queryKey: qk.activity(tenantId ?? undefined, filters),
    queryFn: ({ pageParam }) => {
      const p = new URLSearchParams();
      p.set("limit", String(ACTIVITY_PAGE));
      if (pageParam) p.set("before", pageParam);
      if (filters.source) p.set("source", filters.source);
      if (filters.projectId) p.set("projectId", filters.projectId);
      if (filters.outcome) p.set("outcome", filters.outcome);
      return apiFetch<ActivityPage>(`/api/tenants/${tenantId}/activity?${p.toString()}`);
    },
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextBefore ?? undefined,
    enabled: tenantId !== null,
  });
}
