"use client";
// Portal chrome — design-inventory §2.1 root column, §2.2 top bar, §2.3
// horizontal scroll frame (never reflows below 1280px), §2.4 left nav,
// §2.5 main region, plus the overlay chrome: command palette (§3.16),
// toasts (§2.7), and the right-side flyout panes (§2.8) driven by uiStore.
// Client component: it reads uiStore.openPane to mount the active pane.
import { useState } from "react";
import { useRouter } from "next/navigation";
import { Providers } from "@/components/ui/Providers";
import TopBar from "@/components/chrome/TopBar";
import SideNav from "@/components/chrome/SideNav";
import CommandPalette from "@/components/chrome/CommandPalette";
import { ToastHost } from "@/components/chrome/ToastHost";
import { NotificationsPane } from "@/components/chrome/NotificationsPane";
import { ClusterPane } from "@/components/chrome/ClusterPane";
import { useUiStore } from "@/lib/stores/uiStore";

export default function PortalLayout({ children }: { children: React.ReactNode }) {
  const openPane = useUiStore((s) => s.openPane);
  const setPane = useUiStore((s) => s.setPane);
  const router = useRouter();
  // Pool scope filter — cluster data is not wired yet, so this only holds the
  // "All pools" default until the cluster endpoints land.
  const [selectedPool, setSelectedPool] = useState<string | null>(null);

  const closePane = () => setPane(null);

  return (
    <Providers>
      {/* §2.1 root — full-height column, no page scroll */}
      <div className="flex h-screen flex-col overflow-hidden">
        {/* §2.2 — no user/session endpoint yet: unread 0 (no badge), no chip */}
        <TopBar />
        {/* §2.3 horizontal scroll frame — scrolls sideways below 1280px */}
        <div className="flex flex-1 overflow-x-auto overflow-y-hidden">
          <div className="flex min-w-[1280px] flex-1 overflow-hidden">
            <SideNav />
            {/* §2.5 main content region */}
            <main className="relative flex-1 overflow-y-auto bg-canvas">{children}</main>
          </div>
        </div>
      </div>

      {/* §3.16 — resource search corpus arrives with the resources milestone */}
      <CommandPalette resources={[]} />
      <ToastHost />

      {/* §2.8 — one pane at a time */}
      {openPane === "notif" ? (
        <NotificationsPane
          items={[]} // no notification source wired yet — honest empty state
          onDismissAll={closePane}
          onClose={closePane}
        />
      ) : null}
      {openPane === "tenant" ? (
        <ClusterPane
          // Cluster endpoints arrive in a later milestone — the pane renders
          // its own honest empty states for name/pools.
          pools={[]}
          selectedPool={selectedPool}
          onSelectPool={setSelectedPool}
          onClose={closePane}
          onSignOut={() => {
            closePane();
            router.push("/signin");
          }}
        />
      ) : null}
    </Providers>
  );
}
