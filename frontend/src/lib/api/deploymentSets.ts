"use client";
// Deployment-set data layer (Phase E, ADR-0029/0030). Reads are Reader;
// create/start/stop/delete are Contributor. Sets are tenant-scoped and inherit
// the tenant's authz + audit spine. Modeled on serviceCatalog.ts / tenant.ts.
//
// The routes live behind DEPLOYMENT_SETS_ENABLED on the server: when off (or the
// catalog is not loaded) they 404, exactly like the service catalog. The list &
// detail views treat that 404 as "sets disabled" (an empty/disabled state) rather
// than an error — see isSetsDisabled below.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { ApiError, apiFetch } from "@/lib/api/client";
import type {
  CreateSetRequest,
  CreateSetResponse,
  DeploymentSet,
  DeploymentSetList,
  SetActionResponse,
} from "@/lib/api/generated/types";
import { qk } from "@/lib/api/queryKeys";
import { useActiveTenantId } from "@/lib/stores/uiStore";
import { pushToast } from "@/lib/stores/toastStore";

/**
 * True when a deployment-set query error is the "feature off" 404 rather than a
 * real failure. The list/detail views render a disabled/empty state on this
 * signal instead of an error card (mirrors isCatalogDisabled).
 */
export function isSetsDisabled(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404;
}

/** Don't burn retries on a 4xx (a disabled feature 404s deterministically). */
function noRetryOn4xx(failureCount: number, err: unknown): boolean {
  if (err instanceof ApiError && err.status >= 400 && err.status < 500) return false;
  return failureCount < 2;
}

/** The tenant's deployment sets (GET .../deployment-sets). */
export function useDeploymentSets() {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.deploymentSets(tenantId ?? undefined),
    queryFn: async () => {
      const list = await apiFetch<DeploymentSetList>(`/api/tenants/${tenantId}/deployment-sets`);
      return list.sets;
    },
    enabled: tenantId !== null,
    retry: noRetryOn4xx,
  });
}

/**
 * A single set (GET .../deployment-sets/{setId}). Polls while the set is in a
 * transitional status (provisioning | deleting) — mirroring the deployments/[id]
 * refetchInterval pattern — and is otherwise seeded live by the deployment_set
 * SSE frame (see sse.ts). A cross-tenant/missing set 404s (no existence leak).
 */
export function useDeploymentSet(setId: string | null) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.deploymentSet(tenantId ?? undefined, setId ?? undefined),
    queryFn: () =>
      apiFetch<DeploymentSet>(
        `/api/tenants/${tenantId}/deployment-sets/${encodeURIComponent(setId!)}`,
      ),
    enabled: tenantId !== null && !!setId,
    refetchInterval: (q) => {
      const status = q.state.data?.status;
      return status === "provisioning" || status === "deleting" ? 2500 : false;
    },
    retry: noRetryOn4xx,
  });
}

/** Invalidate the set list + detail after a mutation, plus the resource/quota bars. */
function invalidateSets(qc: ReturnType<typeof useQueryClient>) {
  qc.invalidateQueries({ queryKey: ["deploymentSets"] });
  qc.invalidateQueries({ queryKey: ["resources"] });
  qc.invalidateQueries({ queryKey: ["quota"] });
  qc.invalidateQueries({ queryKey: ["projectQuota"] });
}

/**
 * Provision a deployment set (POST .../deployment-sets → 202 CreateSetResponse).
 * Like useCreateGuest this hook has NO onError toast so the form can route a 409
 * quota_exceeded / conflict to its inline sizing error. The 202 carries the set id
 * and resolved members; it NEVER carries a secret (the cluster join token is not
 * returned — the operator fetches the kubeconfig per the service next-steps).
 */
export function useCreateSet() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: (req: CreateSetRequest) => {
      if (!tenantId) throw new Error("No active tenant selected");
      return apiFetch<CreateSetResponse>(`/api/tenants/${tenantId}/deployment-sets`, {
        method: "POST",
        body: JSON.stringify(req),
      });
    },
    onSuccess: () => invalidateSets(qc),
  });
}

/** Start / stop a whole set (POST .../deployment-sets/{setId}/{action} → 202). */
export function useSetAction() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: ({ setId, action }: { setId: string; action: "start" | "stop" }) => {
      if (!tenantId) throw new Error("No active tenant selected");
      return apiFetch<SetActionResponse>(
        `/api/tenants/${tenantId}/deployment-sets/${encodeURIComponent(setId)}/${action}`,
        { method: "POST", body: "{}" },
      );
    },
    onSuccess: (res, { action }) => {
      pushToast({
        kind: "info",
        title: `${action === "start" ? "Start" : "Stop"} cluster submitted`,
        desc:
          res.tasks.length > 0
            ? `${res.tasks.length} member task(s) queued.`
            : "No eligible members to act on.",
      });
      qc.invalidateQueries({ queryKey: ["deploymentSets"] });
      qc.invalidateQueries({ queryKey: ["resources"] });
      qc.invalidateQueries({ queryKey: ["tasks"] });
    },
    onError: (err, { action }) =>
      pushToast({
        kind: "err",
        title: `Could not ${action} the cluster`,
        desc: err instanceof ApiError ? err.detail : "Request failed",
      }),
  });
}

/**
 * Delete a whole set (DELETE .../deployment-sets/{setId} → 202). DeleteSet purges
 * members directly and expects stopped guests, so the UI confirm-guards this and
 * suggests stopping first when members are still running.
 */
export function useDeleteSet() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: (setId: string) => {
      if (!tenantId) throw new Error("No active tenant selected");
      return apiFetch<SetActionResponse>(
        `/api/tenants/${tenantId}/deployment-sets/${encodeURIComponent(setId)}`,
        { method: "DELETE" },
      );
    },
    onSuccess: (res) => {
      pushToast({
        kind: "info",
        title: "Deleting cluster",
        desc:
          res.tasks.length > 0
            ? `${res.tasks.length} member(s) are being removed.`
            : "The set was removed.",
      });
      invalidateSets(qc);
    },
    onError: (err) =>
      pushToast({
        kind: "err",
        title: "Could not delete the cluster",
        desc: err instanceof ApiError ? err.detail : "Request failed",
      }),
  });
}
