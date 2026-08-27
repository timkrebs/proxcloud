"use client";
// Auto-shutdown schedule data layer (ADR-0019). Resource + project scopes each
// get read / upsert / delete; resource scope also has "skip next occurrence".
// Every call is tenant-scoped like the guest/project queries. A GET that 404s
// means "no schedule set" — a real empty state, resolved to null (NOT an error),
// so the UI shows "No schedule" instead of an error toast.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { ApiError, apiFetch } from "@/lib/api/client";
import type { GuestParams } from "@/lib/api/guestQueries";
import type { Schedule, ScheduleRequest, ScheduleSkipResult } from "@/lib/api/generated/types";
import { qk } from "@/lib/api/queryKeys";
import { useActiveTenantId } from "@/lib/stores/uiStore";
import { pushToast } from "@/lib/stores/toastStore";
import { formatDateTime } from "@/lib/format";

function errDesc(err: unknown): string {
  if (err instanceof ApiError) return err.detail;
  if (err instanceof Error) return err.message;
  return "Request failed";
}

const resourceUrl = (tenantId: string | null, g: GuestParams) => {
  if (!tenantId) throw new Error("No active tenant selected");
  return `/api/tenants/${tenantId}/guests/${encodeURIComponent(g.node)}/${g.type}/${g.vmid}/schedule`;
};
const projectUrl = (tenantId: string | null, projectId: string) => {
  if (!tenantId) throw new Error("No active tenant selected");
  return `/api/tenants/${tenantId}/projects/${encodeURIComponent(projectId)}/schedule`;
};

const resourceId = (g: GuestParams) => `${g.node}/${g.type}/${g.vmid}`;

/** GET a schedule, mapping a 404 to null ("no schedule") rather than throwing. */
async function getScheduleOrNull(url: string): Promise<Schedule | null> {
  try {
    return await apiFetch<Schedule>(url);
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null;
    throw err;
  }
}

/** Invalidate every schedule query + the resource lists (a schedule change can
 *  affect a row badge). Broad "schedule" prefix so both scopes refresh. */
function invalidateSchedules(qc: ReturnType<typeof useQueryClient>) {
  qc.invalidateQueries({ queryKey: ["schedule"] });
  qc.invalidateQueries({ queryKey: ["resources"] });
}

// ── Resource scope ────────────────────────────────────────────────────────────

export function useResourceSchedule(g: GuestParams) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.schedule("resource", resourceId(g), tenantId ?? undefined),
    queryFn: () => getScheduleOrNull(resourceUrl(tenantId, g)),
    enabled: tenantId !== null,
  });
}

export function usePutResourceSchedule(g: GuestParams) {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: (body: ScheduleRequest) =>
      apiFetch<Schedule>(resourceUrl(tenantId, g), { method: "PUT", body: JSON.stringify(body) }),
    onSuccess: () => {
      pushToast({ kind: "info", title: "Schedule saved", desc: `VMID ${g.vmid}` });
      invalidateSchedules(qc);
    },
    onError: (err) =>
      pushToast({ kind: "err", title: "Could not save schedule", desc: errDesc(err) }),
  });
}

export function useDeleteResourceSchedule(g: GuestParams) {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: () => apiFetch<void>(resourceUrl(tenantId, g), { method: "DELETE" }),
    onSuccess: () => {
      pushToast({ kind: "info", title: "Schedule removed", desc: `VMID ${g.vmid}` });
      invalidateSchedules(qc);
    },
    onError: (err) =>
      pushToast({ kind: "err", title: "Could not remove schedule", desc: errDesc(err) }),
  });
}

export function useSkipSchedule(g: GuestParams) {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: () =>
      apiFetch<ScheduleSkipResult>(`${resourceUrl(tenantId, g)}/skip`, {
        method: "POST",
        body: "{}",
      }),
    onSuccess: (res) => {
      const desc =
        res.skipped > 0
          ? res.nextRunAt
            ? `Next auto-shutdown ${formatDateTime(res.nextRunAt)}`
            : `${res.skipped} occurrence${res.skipped === 1 ? "" : "s"} skipped`
          : "No upcoming auto-shutdown to skip.";
      pushToast({ kind: "info", title: "Skip applied", desc });
      invalidateSchedules(qc);
    },
    onError: (err) => pushToast({ kind: "err", title: "Could not skip", desc: errDesc(err) }),
  });
}

// ── Project scope ─────────────────────────────────────────────────────────────

export function useProjectSchedule(projectId: string | null | undefined) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.schedule("project", projectId ?? "", tenantId ?? undefined),
    queryFn: () => getScheduleOrNull(projectUrl(tenantId, projectId!)),
    enabled: tenantId !== null && !!projectId,
  });
}

export function usePutProjectSchedule(projectId: string) {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: (body: ScheduleRequest) =>
      apiFetch<Schedule>(projectUrl(tenantId, projectId), {
        method: "PUT",
        body: JSON.stringify(body),
      }),
    onSuccess: () => {
      pushToast({
        kind: "info",
        title: "Project schedule saved",
        desc: "Applies to guests without an override.",
      });
      invalidateSchedules(qc);
    },
    onError: (err) =>
      pushToast({ kind: "err", title: "Could not save schedule", desc: errDesc(err) }),
  });
}

export function useDeleteProjectSchedule(projectId: string) {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: () => apiFetch<void>(projectUrl(tenantId, projectId), { method: "DELETE" }),
    onSuccess: () => {
      pushToast({
        kind: "info",
        title: "Project schedule removed",
        desc: "Guests keep their own schedules.",
      });
      invalidateSchedules(qc);
    },
    onError: (err) =>
      pushToast({ kind: "err", title: "Could not remove schedule", desc: errDesc(err) }),
  });
}
