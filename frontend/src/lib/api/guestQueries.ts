"use client";
// Guest-detail data layer: queries + mutations for one guest's blades.
// Every call is tenant-scoped (Phase 3): /api/tenants/{tenantId}/guests/…,
// ownership-checked by the backend middleware (cross-tenant VMIDs 404). The
// query key stays tenant-less — VMIDs are cluster-unique — so SSE task-event
// invalidation on ["guest", node, type, vmid] keeps matching.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { ApiError, apiFetch } from "@/lib/api/client";
import type {
  ACLEntry,
  GuestDetail,
  GuestFirewall,
  GuestNICList,
  MetricsResponse,
  Snapshot,
  TaskRef,
  UpdateConfigRequest,
} from "@/lib/api/generated/types";
import { useActiveTenantId } from "@/lib/stores/uiStore";
import { pushToast } from "@/lib/stores/toastStore";

export interface GuestParams {
  node: string;
  type: "qemu" | "lxc";
  vmid: number;
}

const guestBase = (tenantId: string | null, g: GuestParams) => {
  if (!tenantId) throw new Error("No active tenant selected");
  return `/api/tenants/${tenantId}/guests/${encodeURIComponent(g.node)}/${g.type}/${g.vmid}`;
};
export const guestKey = (g: GuestParams) => ["guest", g.node, g.type, g.vmid] as const;

function errDesc(err: unknown): string {
  if (err instanceof ApiError) return err.detail;
  if (err instanceof Error) return err.message;
  return "Request failed";
}

export function useGuest(g: GuestParams) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: guestKey(g),
    queryFn: () => apiFetch<GuestDetail>(guestBase(tenantId, g)),
    refetchInterval: 5000,
    enabled: tenantId !== null,
  });
}

export function useGuestMetrics(g: GuestParams, timeframe = "hour") {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: [...guestKey(g), "metrics", timeframe],
    queryFn: () => apiFetch<MetricsResponse>(`${guestBase(tenantId, g)}/metrics?timeframe=${timeframe}`),
    refetchInterval: 60_000,
    enabled: tenantId !== null,
  });
}

export function useGuestInterfaces(g: GuestParams, enabled = true) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: [...guestKey(g), "interfaces"],
    queryFn: () => apiFetch<GuestNICList>(`${guestBase(tenantId, g)}/interfaces`),
    refetchInterval: 30_000,
    enabled: enabled && tenantId !== null,
  });
}

export function useGuestSnapshots(g: GuestParams) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: [...guestKey(g), "snapshots"],
    queryFn: () => apiFetch<Snapshot[]>(`${guestBase(tenantId, g)}/snapshots`),
    refetchInterval: 15_000,
    enabled: tenantId !== null,
  });
}

export function useGuestFirewall(g: GuestParams) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: [...guestKey(g), "firewall"],
    queryFn: () => apiFetch<GuestFirewall>(`${guestBase(tenantId, g)}/firewall`),
    enabled: tenantId !== null,
  });
}

export function useGuestACL(g: GuestParams) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: [...guestKey(g), "acl"],
    queryFn: () => apiFetch<ACLEntry[]>(`${guestBase(tenantId, g)}/acl`),
    enabled: tenantId !== null,
  });
}

/** Generic guest mutation helper: invalidates the guest tree, toasts errors. */
function useGuestMutation<TVars>(
  g: GuestParams,
  fn: (vars: TVars) => Promise<unknown>,
  okToast?: (vars: TVars) => { title: string; desc: string },
) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: (_res, vars) => {
      if (okToast) pushToast({ kind: "info", ...okToast(vars) });
      qc.invalidateQueries({ queryKey: guestKey(g) });
      qc.invalidateQueries({ queryKey: ["resources"] });
    },
    onError: (err) => pushToast({ kind: "err", title: "Operation failed", desc: errDesc(err) }),
  });
}

export function useUpdateGuestConfig(g: GuestParams) {
  const tenantId = useActiveTenantId();
  return useGuestMutation(
    g,
    (req: UpdateConfigRequest) =>
      apiFetch<TaskRef | undefined>(`${guestBase(tenantId, g)}/config`, {
        method: "PATCH",
        body: JSON.stringify(req),
      }),
    () => ({ title: "Configuration update submitted", desc: `VMID ${g.vmid}` }),
  );
}

export function useResizeDisk(g: GuestParams) {
  const tenantId = useActiveTenantId();
  return useGuestMutation(
    g,
    (req: { disk: string; sizeGib: number }) =>
      apiFetch<TaskRef>(`${guestBase(tenantId, g)}/resize`, { method: "POST", body: JSON.stringify(req) }),
    (req) => ({ title: "Resize submitted", desc: `${req.disk} → ${req.sizeGib} GiB` }),
  );
}

export function useCreateSnapshot(g: GuestParams) {
  const tenantId = useActiveTenantId();
  return useGuestMutation(
    g,
    (req: { name: string; description?: string; vmState?: boolean }) =>
      apiFetch<TaskRef>(`${guestBase(tenantId, g)}/snapshots`, {
        method: "POST",
        body: JSON.stringify({ name: req.name, description: req.description ?? "", vmState: req.vmState ?? false }),
      }),
    (req) => ({ title: "Snapshot started", desc: req.name }),
  );
}

export function useRollbackSnapshot(g: GuestParams) {
  const tenantId = useActiveTenantId();
  return useGuestMutation(
    g,
    (name: string) =>
      apiFetch<TaskRef>(`${guestBase(tenantId, g)}/snapshots/${encodeURIComponent(name)}/rollback`, {
        method: "POST",
        body: "{}",
      }),
    (name) => ({ title: "Rollback started", desc: name }),
  );
}

export function useDeleteSnapshot(g: GuestParams) {
  const tenantId = useActiveTenantId();
  return useGuestMutation(
    g,
    (name: string) =>
      apiFetch<TaskRef>(`${guestBase(tenantId, g)}/snapshots/${encodeURIComponent(name)}`, { method: "DELETE" }),
    (name) => ({ title: "Snapshot deletion started", desc: name }),
  );
}

export function useSetFirewall(g: GuestParams) {
  const tenantId = useActiveTenantId();
  return useGuestMutation(
    g,
    (enable: boolean) =>
      apiFetch(`${guestBase(tenantId, g)}/firewall/options`, {
        method: "PUT",
        body: JSON.stringify({ enable }),
      }),
    (enable) => ({ title: `Firewall ${enable ? "enabled" : "disabled"}`, desc: `VMID ${g.vmid}` }),
  );
}
