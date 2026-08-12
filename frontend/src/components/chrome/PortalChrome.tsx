"use client";
// Data-connected portal chrome: everything inside <Providers> that needs
// live queries — user chip, bell badge + notifications pane, cluster/pool
// pane, palette resource search, and the single SSE connection.
import { useEffect, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";

import CommandPalette, { type PaletteResource } from "@/components/chrome/CommandPalette";
import { ClusterPane } from "@/components/chrome/ClusterPane";
import { NotificationsPane, type NotificationItem } from "@/components/chrome/NotificationsPane";
import SideNav from "@/components/chrome/SideNav";
import { ToastHost } from "@/components/chrome/ToastHost";
import TopBar from "@/components/chrome/TopBar";
import { apiFetch } from "@/lib/api/client";
import { qk } from "@/lib/api/queryKeys";
import { useCluster, useMe, useNodes, useNotifications, usePools, useResources } from "@/lib/api/queries";
import { useEvents } from "@/lib/sse";
import { useUiStore } from "@/lib/stores/uiStore";
import { relativeTime } from "@/lib/format";

function initialsOf(name: string): string {
  const parts = name.split(/[\s._@-]+/).filter(Boolean);
  const chars = parts.length >= 2 ? [parts[0][0], parts[1][0]] : [name[0] ?? "", name[1] ?? ""];
  return chars.join("").toUpperCase();
}

export default function PortalChrome({ children }: { children: React.ReactNode }) {
  const openPane = useUiStore((s) => s.openPane);
  const setPane = useUiStore((s) => s.setPane);
  const router = useRouter();
  const qc = useQueryClient();
  const [selectedPool, setSelectedPool] = useState<string | null>(null);

  useEvents();

  const me = useMe();
  const cluster = useCluster();
  const nodes = useNodes();
  const pools = usePools();
  const notifications = useNotifications();
  const resources = useResources();

  const notifItems: NotificationItem[] = (notifications.data ?? []).map((n) => ({
    id: n.id,
    title: n.title,
    desc: n.detail,
    time: relativeTime(n.createdAt),
    kind: n.kind as NotificationItem["kind"],
  }));
  const unread = (notifications.data ?? []).filter((n) => !n.read).length;

  // Opening the bell zeroes the badge (design §3.13): mark all unread read.
  useEffect(() => {
    if (openPane !== "notif" || unread === 0) return;
    const ids = (notifications.data ?? []).filter((n) => !n.read).map((n) => n.id);
    apiFetch("/api/notifications/read", { method: "POST", body: JSON.stringify({ ids }) })
      .then(() => qc.invalidateQueries({ queryKey: qk.notifications }))
      .catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [openPane]);

  const paletteResources: PaletteResource[] = (resources.data ?? [])
    .filter((g) => !g.template)
    .map((g) => ({
      id: g.id,
      name: g.name,
      type: g.type === "qemu" ? "Virtual machine" : "LXC container",
      node: g.node,
      kind: g.type === "qemu" ? ("vm" as const) : ("lxc" as const),
    }));

  // Standalone nodes have no corosync cluster name — the node's own name is
  // the honest identity to show.
  const clusterName = cluster.data?.name || nodes.data?.[0]?.node;

  const closePane = () => setPane(null);
  const signOut = async () => {
    closePane();
    try {
      await apiFetch("/api/auth/logout", { method: "POST", body: "{}" });
    } finally {
      router.push("/signin");
    }
  };

  return (
    <>
      <div className="flex h-screen flex-col overflow-hidden">
        <TopBar
          unread={unread}
          displayName={me.data ? me.data.displayName || me.data.email : undefined}
          initials={me.data ? initialsOf(me.data.displayName || me.data.email) : undefined}
        />
        <div className="flex flex-1 overflow-x-auto overflow-y-hidden">
          <div className="flex min-w-[1280px] flex-1 overflow-hidden">
            <SideNav />
            <main className="relative flex-1 overflow-y-auto bg-canvas">{children}</main>
          </div>
        </div>
      </div>

      <CommandPalette resources={paletteResources} />
      <ToastHost />

      {openPane === "notif" ? (
        <NotificationsPane
          items={notifItems}
          onDismissAll={() => {
            const ids = (notifications.data ?? []).map((n) => n.id);
            if (ids.length > 0) {
              apiFetch("/api/notifications/read", { method: "POST", body: JSON.stringify({ ids }) })
                .then(() => qc.invalidateQueries({ queryKey: qk.notifications }))
                .catch(() => {});
            }
            closePane();
          }}
          onClose={closePane}
        />
      ) : null}
      {openPane === "tenant" ? (
        <ClusterPane
          clusterName={clusterName}
          online={(cluster.data?.nodesOnline ?? 0) > 0}
          pools={(pools.data ?? []).map((p) => ({ poolid: p.poolId, comment: p.comment }))}
          selectedPool={selectedPool}
          onSelectPool={setSelectedPool}
          onClose={closePane}
          onSignOut={signOut}
        />
      ) : null}
    </>
  );
}
