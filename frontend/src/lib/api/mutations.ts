"use client";
// Lifecycle mutations. Every action is async on the backend (202 + real
// UPID); completion arrives via SSE task events, which invalidate queries
// and toast the outcome. Failures surface the verbatim PVE error.
// Phase 3: guest routes are tenant-scoped and ownership-checked by the backend.
import { useMutation, useQueryClient } from "@tanstack/react-query";

import { ApiError, apiFetch } from "@/lib/api/client";
import type { CreateGuestRequest, CreateGuestResponse, TaskRef } from "@/lib/api/generated/types";
import { useActiveTenantId } from "@/lib/stores/uiStore";
import { pushToast } from "@/lib/stores/toastStore";

export type GuestAction = "start" | "stop" | "shutdown" | "reboot" | "reset";

/**
 * True for a 409 over-quota rejection (distinct from a duplicate-VMID conflict).
 * The wizard keys on this to surface a distinct inline sizing error instead of
 * the generic conflict toast (contract: code === "quota_exceeded").
 */
export function isQuotaExceeded(err: unknown): boolean {
  return err instanceof ApiError && err.code === "quota_exceeded";
}

/**
 * Invalidate the quota bars after a create/delete so used-vs-limit refreshes.
 * Prefix keys (["quota"] / ["projectQuota"]) so it hits every tenant/project
 * entry without needing the specific ids at the call site.
 */
function invalidateQuota(qc: ReturnType<typeof useQueryClient>) {
  qc.invalidateQueries({ queryKey: ["quota"] });
  qc.invalidateQueries({ queryKey: ["projectQuota"] });
}

export interface GuestTarget {
  node: string;
  type: "qemu" | "lxc";
  vmid: number;
  name?: string;
}

function errDesc(err: unknown): string {
  if (err instanceof ApiError) return err.detail;
  if (err instanceof Error) return err.message;
  return "Request failed";
}

const guestBase = (tenantId: string | null, t: GuestTarget) => {
  if (!tenantId) throw new Error("No active tenant selected");
  return `/api/tenants/${tenantId}/guests/${encodeURIComponent(t.node)}/${t.type}/${t.vmid}`;
};

/**
 * Submit the wizard's CreateGuestRequest. onSuccess invalidates resources + the
 * quota bars; the hook deliberately has NO onError toast so the wizard can route
 * a 409 quota_exceeded to its inline sizing error (isQuotaExceeded) rather than
 * the generic conflict toast. The caller supplies onSuccess (routing) and
 * onError (inline vs toast) via mutate() options.
 */
export function useCreateGuest() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: (req: CreateGuestRequest) => {
      if (!tenantId) throw new Error("No active tenant selected");
      return apiFetch<CreateGuestResponse>(`/api/tenants/${tenantId}/guests`, {
        method: "POST",
        body: JSON.stringify(req),
      });
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["resources"] });
      invalidateQuota(qc);
    },
  });
}

export function useGuestAction() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: ({ target, action }: { target: GuestTarget; action: GuestAction }) =>
      apiFetch<TaskRef>(`${guestBase(tenantId, target)}/${action}`, { method: "POST", body: "{}" }),
    onSuccess: (ref, { target }) => {
      pushToast({
        kind: "info",
        title: `${ref.action} submitted`,
        desc: target.name ? `${target.name} (VMID ${target.vmid})` : `VMID ${target.vmid}`,
      });
      qc.invalidateQueries({ queryKey: ["resources"] });
      qc.invalidateQueries({ queryKey: ["guest", target.node, target.type, target.vmid] });
    },
    onError: (err, { action, target }) => {
      pushToast({
        kind: "err",
        title: `Could not ${action} VMID ${target.vmid}`,
        desc: errDesc(err),
      });
    },
  });
}

export function useDeleteGuest() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    // The typed name travels to the server, which re-verifies it against
    // the live guest — the confirmation is not just a UI gate.
    mutationFn: ({
      target,
      purge,
      confirmName,
    }: {
      target: GuestTarget;
      purge: boolean;
      confirmName: string;
    }) =>
      apiFetch<TaskRef>(`${guestBase(tenantId, target)}${purge ? "?purge=1" : ""}`, {
        method: "DELETE",
        body: JSON.stringify({ confirmName }),
      }),
    onSuccess: (_ref, { target }) => {
      pushToast({
        kind: "info",
        title: `Deleting ${target.name ?? `VMID ${target.vmid}`}`,
        desc: "The guest and its disks are being removed.",
      });
      qc.invalidateQueries({ queryKey: ["resources"] });
      // The freed reservation returns capacity — refresh the usage bars.
      invalidateQuota(qc);
    },
    onError: (err, { target }) => {
      pushToast({
        kind: "err",
        title: `Could not delete ${target.name ?? `VMID ${target.vmid}`}`,
        desc: errDesc(err),
      });
    },
  });
}
