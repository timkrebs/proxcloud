"use client";
// Guest-detail data layer: queries + mutations for one guest's blades.
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
import { pushToast } from "@/lib/stores/toastStore";

export interface GuestParams {
  node: string;
  type: "qemu" | "lxc";
  vmid: number;
}

const base = (g: GuestParams) => `/api/guests/${g.node}/${g.type}/${g.vmid}`;
export const guestKey = (g: GuestParams) => ["guest", g.node, g.type, g.vmid] as const;

function errDesc(err: unknown): string {
  if (err instanceof ApiError) return err.detail;
  if (err instanceof Error) return err.message;
  return "Request failed";
}

export function useGuest(g: GuestParams) {
  return useQuery({
    queryKey: guestKey(g),
    queryFn: () => apiFetch<GuestDetail>(base(g)),
    refetchInterval: 5000,
  });
}

export function useGuestMetrics(g: GuestParams, timeframe = "hour") {
  return useQuery({
    queryKey: [...guestKey(g), "metrics", timeframe],
    queryFn: () => apiFetch<MetricsResponse>(`${base(g)}/metrics?timeframe=${timeframe}`),
    refetchInterval: 60_000,
  });
}

export function useGuestInterfaces(g: GuestParams, enabled = true) {
  return useQuery({
    queryKey: [...guestKey(g), "interfaces"],
    queryFn: () => apiFetch<GuestNICList>(`${base(g)}/interfaces`),
    refetchInterval: 30_000,
    enabled,
  });
}

export function useGuestSnapshots(g: GuestParams) {
  return useQuery({
    queryKey: [...guestKey(g), "snapshots"],
    queryFn: () => apiFetch<Snapshot[]>(`${base(g)}/snapshots`),
    refetchInterval: 15_000,
  });
}

export function useGuestFirewall(g: GuestParams) {
  return useQuery({
    queryKey: [...guestKey(g), "firewall"],
    queryFn: () => apiFetch<GuestFirewall>(`${base(g)}/firewall`),
  });
}

export function useGuestACL(g: GuestParams) {
  return useQuery({
    queryKey: [...guestKey(g), "acl"],
    queryFn: () => apiFetch<ACLEntry[]>(`${base(g)}/acl`),
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
  return useGuestMutation(
    g,
    (req: UpdateConfigRequest) =>
      apiFetch<TaskRef | undefined>(`${base(g)}/config`, { method: "PATCH", body: JSON.stringify(req) }),
    () => ({ title: "Configuration update submitted", desc: `VMID ${g.vmid}` }),
  );
}

export function useResizeDisk(g: GuestParams) {
  return useGuestMutation(
    g,
    (req: { disk: string; sizeGib: number }) =>
      apiFetch<TaskRef>(`${base(g)}/resize`, { method: "POST", body: JSON.stringify(req) }),
    (req) => ({ title: "Resize submitted", desc: `${req.disk} → ${req.sizeGib} GiB` }),
  );
}

export function useCreateSnapshot(g: GuestParams) {
  return useGuestMutation(
    g,
    (req: { name: string; description?: string; vmState?: boolean }) =>
      apiFetch<TaskRef>(`${base(g)}/snapshots`, {
        method: "POST",
        body: JSON.stringify({ name: req.name, description: req.description ?? "", vmState: req.vmState ?? false }),
      }),
    (req) => ({ title: "Snapshot started", desc: req.name }),
  );
}

export function useRollbackSnapshot(g: GuestParams) {
  return useGuestMutation(
    g,
    (name: string) =>
      apiFetch<TaskRef>(`${base(g)}/snapshots/${encodeURIComponent(name)}/rollback`, {
        method: "POST",
        body: "{}",
      }),
    (name) => ({ title: "Rollback started", desc: name }),
  );
}

export function useDeleteSnapshot(g: GuestParams) {
  return useGuestMutation(
    g,
    (name: string) =>
      apiFetch<TaskRef>(`${base(g)}/snapshots/${encodeURIComponent(name)}`, { method: "DELETE" }),
    (name) => ({ title: "Snapshot deletion started", desc: name }),
  );
}

export function useSetFirewall(g: GuestParams) {
  return useGuestMutation(
    g,
    (enable: boolean) =>
      apiFetch(`${base(g)}/firewall/options`, { method: "PUT", body: JSON.stringify({ enable }) }),
    (enable) => ({ title: `Firewall ${enable ? "enabled" : "disabled"}`, desc: `VMID ${g.vmid}` }),
  );
}
