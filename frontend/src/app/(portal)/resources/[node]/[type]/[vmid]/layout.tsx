"use client";
// Guest blade — design-inventory §3.5: breadcrumb, title row with status
// pill, status-gated command bar, 210px resource menu, sub-blade content.
// Delete flyout (§3.14) and resource-JSON flyout (§3.15) mount here.
import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { DeleteFlyout } from "@/components/guest/DeleteFlyout";
import { useGuestParams } from "@/components/guest/common";
import { Flyout } from "@/components/ui/Flyout";
import { StatusPill } from "@/components/ui/StatusPill";
import { Fi, Mi, Svc, type MiName } from "@/components/ui/icons";
import { useGuest } from "@/lib/api/guestQueries";
import { useGuestAction, type GuestAction } from "@/lib/api/mutations";
import { commandBarState } from "@/lib/status";
import { recordRecent } from "@/lib/stores/recent";

interface MenuItem {
  label: string;
  icon: MiName;
  seg: string; // route segment; "" = overview
}

const MENU: { title?: string; items: MenuItem[] }[] = [
  {
    items: [
      { label: "Overview", icon: "grid", seg: "" },
      { label: "Console", icon: "console", seg: "console" },
      { label: "Activity log", icon: "clock", seg: "activity" },
      { label: "Access control (IAM)", icon: "person", seg: "access" },
      { label: "Tags", icon: "tag", seg: "tags" },
    ],
  },
  {
    title: "SETTINGS",
    items: [
      { label: "Networking", icon: "globe", seg: "networking" },
      { label: "Disks", icon: "disk", seg: "disks" },
      { label: "Snapshots", icon: "camera", seg: "snapshots" },
      { label: "Size", icon: "resize", seg: "size" },
      { label: "Schedule", icon: "clock", seg: "schedule" },
      { label: "Lifecycle", icon: "bolt", seg: "ttl" },
    ],
  },
  {
    title: "MONITORING",
    items: [{ label: "Metrics", icon: "chart", seg: "metrics" }],
  },
];

export default function GuestBladeLayout({ children }: { children: React.ReactNode }) {
  const g = useGuestParams();
  const pathname = usePathname();
  const router = useRouter();
  const guest = useGuest(g);
  const action = useGuestAction();
  const [flyout, setFlyout] = useState<null | "delete" | "json">(null);
  const [filter, setFilter] = useState("");

  const name = guest.data?.name ?? `VMID ${g.vmid}`;
  const status = guest.data?.status ?? "";
  const bar = commandBarState(status);
  const kindLabel = g.type === "qemu" ? "Virtual machine" : "LXC container";
  const basePath = `/resources/${g.node}/${g.type}/${g.vmid}`;

  useEffect(() => {
    if (guest.data) {
      recordRecent({ id: guest.data.id, type: g.type, vmid: g.vmid, name: guest.data.name, node: g.node });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [guest.data?.id]);

  const run = (act: GuestAction) =>
    action.mutate({ target: { ...g, name }, action: act });

  const commands = useMemo(
    () => [
      { label: "Connect", icon: <Mi name="console" size={14} />, enabled: bar.connect, onClick: () => router.push(`${basePath}/console`), sep: false },
      { label: "Start", icon: <Fi name="play" size={13} color="currentColor" />, enabled: bar.start, onClick: () => run("start"), sep: true },
      { label: "Restart", icon: <Mi name="restart" size={14} />, enabled: bar.restart, onClick: () => run("reboot"), sep: false },
      { label: "Stop", icon: <Fi name="stop" size={13} color="currentColor" />, enabled: bar.stop, onClick: () => run("stop"), sep: false },
      { label: "Delete", icon: <Mi name="trash" size={14} />, enabled: bar.delete, onClick: () => setFlyout("delete"), sep: true },
      { label: "Refresh", icon: <Mi name="restart" size={14} />, enabled: true, onClick: () => guest.refetch(), sep: true },
    ],
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [bar.connect, bar.start, bar.restart, bar.stop, bar.delete, name],
  );

  const menuGroups = MENU.map((grp) => ({
    ...grp,
    items: grp.items.filter((it) => filter === "" || it.label.toLowerCase().includes(filter.toLowerCase())),
  })).filter((grp) => grp.items.length > 0);

  return (
    <div className="flex min-h-full flex-col">
      <div className="px-8 pt-5">
        <nav className="mb-[10px] text-[12px]">
          <Link href="/dashboard">Home</Link>
          <span className="text-ink-2"> &gt; </span>
          <Link href={`/resources?type=${g.type}`}>{kindLabel}s</Link>
          <span className="text-ink-2"> &gt; </span>
          <span className="text-ink-2">{name}</span>
        </nav>

        <div className="flex items-center gap-3">
          <Svc name={g.type === "qemu" ? "vm" : "lxc"} size={30} />
          <h1 className="text-[24px] font-semibold">{name}</h1>
          {status ? <StatusPill status={status} /> : null}
        </div>
        <p className="mt-[2px] text-[12px] text-ink-2">
          {kindLabel} · VMID {g.vmid} · {g.node}
        </p>

        {/* command bar §3.5 */}
        <div className="mt-3 flex items-center border-b border-line">
          {commands.map((c) => (
            <span key={c.label} className="flex items-center">
              {c.sep ? <span className="mx-1 h-[18px] w-px self-center bg-line" aria-hidden /> : null}
              <button
                type="button"
                disabled={!c.enabled}
                title={c.label}
                onClick={c.onClick}
                className={`flex h-9 items-center gap-[6px] border-none bg-transparent px-[10px] text-[13px] ${
                  c.enabled ? "cursor-pointer text-ink hover:bg-hover" : "cursor-default text-ink-3"
                }`}
              >
                {c.icon}
                {c.label}
              </button>
            </span>
          ))}
        </div>
      </div>

      <div className="flex flex-1 items-stretch">
        {/* resource menu §3.5 */}
        <aside className="w-[210px] flex-none border-r border-line bg-card py-[10px]">
          <div className="px-[10px] pb-2">
            <input
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              placeholder="Search"
              aria-label="Filter menu"
              className="h-7 w-full rounded-fluent border border-line-input bg-card px-2 text-[12px] outline-none focus:border-accent"
            />
          </div>
          {menuGroups.map((grp, gi) => (
            <div key={gi}>
              {grp.title ? (
                <div className="px-3 pt-[10px] pb-1 text-[11px] font-semibold tracking-[.3px] text-ink-2 uppercase">
                  {grp.title}
                </div>
              ) : null}
              {grp.items.map((it) => {
                const href = it.seg === "" ? basePath : `${basePath}/${it.seg}`;
                const active = pathname === href;
                return (
                  <Link
                    key={it.label}
                    href={href}
                    className={`flex h-8 items-center gap-2 px-3 text-[13px] text-ink hover:bg-hover hover:no-underline ${
                      active ? "bg-nav-active" : ""
                    }`}
                  >
                    <Mi name={it.icon} size={16} color={active ? "var(--color-accent)" : "var(--color-ink-2)"} />
                    {it.label}
                  </Link>
                );
              })}
            </div>
          ))}
        </aside>

        <section className="min-w-0 flex-1 px-7 pt-[18px] pb-10">
          {guest.isError ? <CardError err={guest.error} /> : guest.isPending ? <Skeleton className="h-40" /> : children}
        </section>
      </div>

      {flyout === "delete" ? (
        <DeleteFlyout guest={g} name={name} running={status === "running"} onClose={() => setFlyout(null)} />
      ) : null}
      {flyout === "json" ? (
        <Flyout title="Resource JSON" width={440} onClose={() => setFlyout(null)}>
          <pre className="rounded-fluent border border-line bg-hover p-3 font-mono text-[12px] leading-[1.5] whitespace-pre-wrap">
            {JSON.stringify(guest.data, null, 2)}
          </pre>
        </Flyout>
      ) : null}

      {/* Overview opens the JSON pane via a custom event to avoid prop drilling. */}
      <JsonPaneListener onOpen={() => setFlyout("json")} />
    </div>
  );
}

function JsonPaneListener({ onOpen }: { onOpen: () => void }) {
  useEffect(() => {
    const h = () => onOpen();
    window.addEventListener("proxcloud:open-json", h);
    return () => window.removeEventListener("proxcloud:open-json", h);
  }, [onOpen]);
  return null;
}
