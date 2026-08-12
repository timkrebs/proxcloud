"use client";
// Single SSE connection for the whole portal: node metrics flow straight
// into the query cache; task events invalidate the affected queries and
// surface completion toasts with the real Proxmox exit status.
import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";

import type { MetricsEvent, TaskEvent } from "@/lib/api/generated/types";
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

    // EventSource reconnects automatically (server sends retry: 5000).
    return () => es.close();
  }, [qc, activeTenantId]);
}

/** Read the latest live metrics event out of the cache (set by useEvents). */
export function liveMetricsKey() {
  return qk.liveMetrics;
}
