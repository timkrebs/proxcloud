"use client";
// TTL / ephemeral-resource data layer (ADR-0020). A guest can carry a TTL
// (stop or delete on expiry); a project governs a default + a hard max. Every
// call is tenant-scoped like the guest/schedule queries. A guest-TTL GET that
// 404s means "no TTL set" — a real empty state, resolved to null (NOT an error),
// so the blade shows "No TTL — this guest is permanent". The project TTL policy
// GET never 404s (the backend returns a default {maxTtlSeconds} when unset).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { ApiError, apiFetch } from "@/lib/api/client";
import type { GuestParams } from "@/lib/api/guestQueries";
import type {
  Ttl,
  TtlExtendResult,
  TtlPolicy,
  TtlPolicyRequest,
  TtlRequest,
} from "@/lib/api/generated/types";
import { qk } from "@/lib/api/queryKeys";
import { useActiveTenantId } from "@/lib/stores/uiStore";
import { pushToast } from "@/lib/stores/toastStore";
import { formatDateTime } from "@/lib/format";

function errDesc(err: unknown): string {
  if (err instanceof ApiError) return err.detail;
  if (err instanceof Error) return err.message;
  return "Request failed";
}

const guestUrl = (tenantId: string | null, g: GuestParams) => {
  if (!tenantId) throw new Error("No active tenant selected");
  return `/api/tenants/${tenantId}/guests/${encodeURIComponent(g.node)}/${g.type}/${g.vmid}/ttl`;
};
const policyUrl = (tenantId: string | null, projectId: string) => {
  if (!tenantId) throw new Error("No active tenant selected");
  return `/api/tenants/${tenantId}/projects/${encodeURIComponent(projectId)}/ttl-policy`;
};
const listUrl = (tenantId: string | null, projectId: string) => {
  if (!tenantId) throw new Error("No active tenant selected");
  return `/api/tenants/${tenantId}/projects/${encodeURIComponent(projectId)}/ttls`;
};

const resourceId = (g: GuestParams) => `${g.node}/${g.type}/${g.vmid}`;

/** GET a guest TTL, mapping a 404 to null ("no TTL") rather than throwing.
 *  Exported for unit testing the 404→null contract. */
export async function getTtlOrNull(url: string): Promise<Ttl | null> {
  try {
    return await apiFetch<Ttl>(url);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null;
    throw err;
  }
}

/** Invalidate every TTL query + the resource lists (a TTL change can affect a
 *  row's derived state). Broad "ttl" prefix so all three scopes refresh. */
function invalidateTtls(qc: ReturnType<typeof useQueryClient>) {
  qc.invalidateQueries({ queryKey: ["ttl"] });
  qc.invalidateQueries({ queryKey: ["resources"] });
}

// ── Guest scope ───────────────────────────────────────────────────────────────

export function useGuestTtl(g: GuestParams) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.ttl("guest", resourceId(g), tenantId ?? undefined),
    queryFn: () => getTtlOrNull(guestUrl(tenantId, g)),
    enabled: tenantId !== null,
  });
}

export function usePutGuestTtl(g: GuestParams) {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: (body: TtlRequest) =>
      apiFetch<Ttl>(guestUrl(tenantId, g), { method: "PUT", body: JSON.stringify(body) }),
    onSuccess: (ttl) => {
      pushToast({
        kind: "info",
        title: ttl.action === "delete" ? "Delete TTL set" : "TTL set",
        desc: `VMID ${g.vmid} · expires ${formatDateTime(ttl.expiresAt)}`,
      });
      invalidateTtls(qc);
    },
    onError: (err) => pushToast({ kind: "err", title: "Could not set TTL", desc: errDesc(err) }),
  });
}

export function useDeleteGuestTtl(g: GuestParams) {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: () => apiFetch<void>(guestUrl(tenantId, g), { method: "DELETE" }),
    onSuccess: () => {
      pushToast({ kind: "info", title: "TTL removed", desc: `VMID ${g.vmid} is now permanent.` });
      invalidateTtls(qc);
    },
    onError: (err) => pushToast({ kind: "err", title: "Could not remove TTL", desc: errDesc(err) }),
  });
}

export function useExtendGuestTtl(g: GuestParams) {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: () =>
      apiFetch<TtlExtendResult>(`${guestUrl(tenantId, g)}/extend`, { method: "POST", body: "{}" }),
    onSuccess: (res) => {
      pushToast({
        kind: "info",
        title: "TTL extended",
        desc: `New expiry ${formatDateTime(res.expiresAt)}`,
      });
      invalidateTtls(qc);
    },
    onError: (err) => pushToast({ kind: "err", title: "Could not extend TTL", desc: errDesc(err) }),
  });
}

// ── Project scope ─────────────────────────────────────────────────────────────

/** The project TTL policy. GET never 404s — an unset policy returns a default
 *  `{maxTtlSeconds}` with no default TTL, so there is no null branch here. */
export function useProjectTtlPolicy(projectId: string | null | undefined) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.ttl("policy", projectId ?? "", tenantId ?? undefined),
    queryFn: () => apiFetch<TtlPolicy>(policyUrl(tenantId, projectId!)),
    enabled: tenantId !== null && !!projectId,
  });
}

export function usePutProjectTtlPolicy(projectId: string) {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: (body: TtlPolicyRequest) =>
      apiFetch<TtlPolicy>(policyUrl(tenantId, projectId), {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      pushToast({
        kind: "info",
        title: "TTL policy saved",
        desc: "Applies to new and extended TTLs.",
      });
      invalidateTtls(qc);
    },
    onError: (err) =>
      pushToast({ kind: "err", title: "Could not save TTL policy", desc: errDesc(err) }),
  });
}

/** The project's TTLs (expiring-soon / expired view), ordered by expiry. */
export function useProjectTtls(projectId: string | null | undefined) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.ttl("list", projectId ?? "", tenantId ?? undefined),
    queryFn: () => apiFetch<Ttl[]>(listUrl(tenantId, projectId!)),
    enabled: tenantId !== null && !!projectId,
  });
}
