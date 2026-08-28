"use client";
// Data-connected portal chrome: everything inside <Providers> that needs
// live queries — user chip, bell badge + notifications pane, the directory
// (tenant + project) switcher, palette resource search, and the single SSE
// connection. Also hydrates the active tenant from /api/auth/me.
import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useRouter } from "next/navigation";

import CommandPalette, { type PaletteResource } from "@/components/chrome/CommandPalette";
import { TenantPane } from "@/components/chrome/ClusterPane";
import { NotificationsPane, type NotificationItem } from "@/components/chrome/NotificationsPane";
import SideNav from "@/components/chrome/SideNav";
import { ToastHost } from "@/components/chrome/ToastHost";
import TopBar from "@/components/chrome/TopBar";
import { apiFetch } from "@/lib/api/client";
import { qk } from "@/lib/api/queryKeys";
import { useMe, useNotifications, useResources } from "@/lib/api/queries";
import { useProjects, useSwitchTenant } from "@/lib/api/tenant";
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
  const activeTenantId = useUiStore((s) => s.activeTenantId);
  const setActiveTenant = useUiStore((s) => s.setActiveTenant);
  const projectFilter = useUiStore((s) => s.projectFilter);
  const setProjectFilter = useUiStore((s) => s.setProjectFilter);
  const router = useRouter();
  const qc = useQueryClient();

  useEvents();

  const me = useMe();
  const notifications = useNotifications();
  const resources = useResources();
  const projects = useProjects();
  const switchTenant = useSwitchTenant();

  // Hydrate the active tenant once /api/auth/me is known: prefer the persisted
  // id iff still a member, else the session's active tenant, else the first
  // reachable tenant. Guarded so it only writes when the resolved id actually
  // changes (never spuriously clears the project filter).
  useEffect(() => {
    if (!me.data) return;
    const tenants = me.data.tenants ?? [];
    const isMember = (id: string | null | undefined): boolean =>
      !!id && tenants.some((t) => t.id === id);
    const persisted = useUiStore.getState().activeTenantId;
    let next: string | null = null;
    if (isMember(persisted)) next = persisted;
    else if (isMember(me.data.activeTenantId)) next = me.data.activeTenantId;
    else if (tenants.length > 0) next = tenants[0].id;
    if (next !== useUiStore.getState().activeTenantId) setActiveTenant(next);
  }, [me.data, setActiveTenant]);

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
        <TenantPane
          tenants={me.data?.tenants ?? []}
          activeTenantId={activeTenantId}
          onSelectTenant={(id) => switchTenant.mutate(id)}
          switching={switchTenant.isPending}
          projects={projects.data ?? []}
          projectsPending={projects.isPending && activeTenantId !== null}
          projectsError={projects.error}
          selectedProjectId={projectFilter}
          onSelectProject={setProjectFilter}
          onClose={closePane}
          onSignOut={signOut}
        />
      ) : null}
    </>
  );
}
