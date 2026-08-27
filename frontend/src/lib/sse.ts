"use client";
// Single SSE connection for the whole portal: node metrics flow straight
// into the query cache; task events invalidate the affected queries and
// surface completion toasts with the real Proxmox exit status.
import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";

import type {
  MetricsEvent,
  ScheduleWarningEvent,
  TaskEvent,
  TtlWarningEvent,
} from "@/lib/api/generated/types";
import { qk } from "@/lib/api/queryKeys";
import { useActiveTenantId } from "@/lib/stores/uiStore";
import { pushToast } from "@/lib/stores/toastStore";

export function useEvents() {
  const qc = useQueryClient();
  // The server derives the SSE ownership filter from the session's active
  // tenant, so a tenant switch must reopen the stream — otherwise task events
  // for the old tenant would keep flowing. Reconnect whenever it changes.
  const activeTenantId = useActiveTenantId();

  useEffect(() => {
    const es = new EventSource("/api/events");

    es.addEventListener("metrics", (ev) => {
      try {
        const data = JSON.parse((ev as MessageEvent).data) as MetricsEvent;
        qc.setQueryData(qk.liveMetrics, data);
      } catch {
        // malformed frame — ignore, next tick replaces it
      }
    });

    es.addEventListener("task", (ev) => {
      let data: TaskEvent;
      try {
        data = JSON.parse((ev as MessageEvent).data) as TaskEvent;
      } catch {
        return;
      }
      // Any task state change can affect lists and the bell.
      qc.invalidateQueries({ queryKey: ["resources"] });
      qc.invalidateQueries({ queryKey: ["tasks"] });
      qc.invalidateQueries({ queryKey: qk.notifications });
      if (data.resource) {
        qc.invalidateQueries({ queryKey: ["guest", data.resource.node, data.resource.type, data.resource.vmid] });
      }

      const name = data.resource?.name || (data.resource ? `VMID ${data.resource.vmid}` : "");
      if (data.status === "succeeded") {
        pushToast({ kind: "ok", title: `${data.action} complete`, desc: name });
      } else if (data.status === "failed") {
        pushToast({ kind: "err", title: `${data.action} failed`, desc: data.exitStatus || name });
      }
    });

    // Auto-shutdown heads-up (T-15m): the backend scopes this to the owning
    // tenant, so it is only delivered to users who can act on it. Toast the
    // warning and refresh the schedule/resources caches (a fresh next-run).
    es.addEventListener("schedule_warning", (ev) => {
      let data: ScheduleWarningEvent;
      try {
        data = JSON.parse((ev as MessageEvent).data) as ScheduleWarningEvent;
      } catch {
        return;
      }
      pushToast({ kind: "info", title: data.title, desc: data.detail });
      qc.invalidateQueries({ queryKey: ["schedule"] });
      qc.invalidateQueries({ queryKey: ["resources"] });
    });

    // TTL heads-up (T-24h / T-1h): the backend scopes this to the owning tenant,
    // so it only reaches users who can act on it. Toast an extend-oriented
    // message and refresh the ttl/resources caches (a fresh expiry).
    es.addEventListener("ttl_warning", (ev) => {
      let data: TtlWarningEvent;
      try {
        data = JSON.parse((ev as MessageEvent).data) as TtlWarningEvent;
      } catch {
        return;
      }
      const verb = data.action === "delete" ? "be deleted" : "stop";
      const when = data.which === "1h" ? "within an hour" : "within 24 hours";
      pushToast({
        kind: "info",
        title: `Guest ${data.vmid} expires soon`,
        desc: `It will ${verb} ${when} — extend from its Lifecycle settings.`,
      });
      qc.invalidateQueries({ queryKey: ["ttl"] });
      qc.invalidateQueries({ queryKey: ["resources"] });
    });

    // EventSource reconnects automatically (server sends retry: 5000).
    return () => es.close();
  }, [qc, activeTenantId]);
}

/** Read the latest live metrics event out of the cache (set by useEvents). */
export function liveMetricsKey() {
  return qk.liveMetrics;
}
