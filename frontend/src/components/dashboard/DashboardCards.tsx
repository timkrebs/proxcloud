"use client";
// Dashboard widgets — design-inventory §3.1, adapted to real Proxmox data.
// Every number comes from the API; loading renders pulse skeletons matching
// the final layout, failures render the real error (incl. verbatim PVE text).
import Link from "next/link";
import { useRouter } from "next/navigation";

import { Card } from "@/components/ui/Card";
import { ProgressBar } from "@/components/ui/ProgressBar";
import { Sparkline } from "@/components/ui/Sparkline";
import { StatusDot } from "@/components/ui/StatusDot";
import { Mi, Svc, type SvcName } from "@/components/ui/icons";
import { ApiError } from "@/lib/api/client";
import type { GuestSummary } from "@/lib/api/generated/types";
import { useCluster, useHealth, useNodeMetrics, useNodes, useResources } from "@/lib/api/queries";
import { formatBytesPair, formatPct, formatUptime, relativeTime } from "@/lib/format";
import { listRecent } from "@/lib/stores/recent";

// ── shared bits ──────────────────────────────────────────────────────────────

function errorDetail(err: unknown): string {
  if (err instanceof ApiError) return err.detail;
  if (err instanceof Error) return err.message;
  return "Request failed";
}

export function CardError({ err }: { err: unknown }) {
  return (
    <div className="flex items-start gap-2 py-2">
      <Mi name="warn" size={16} color="var(--color-err)" />
      <span className="text-[13px] leading-[1.4] text-ink-2">{errorDetail(err)}</span>
    </div>
  );
}

export function Skeleton({ className = "" }: { className?: string }) {
  return <div className={`animate-pulse rounded-fluent bg-hover ${className}`} aria-hidden />;
}

// ── §3.1 service tiles ───────────────────────────────────────────────────────

const TILES: { label: string; icon: SvcName | "plus"; href: string }[] = [
  { label: "Virtual machine", icon: "vm", href: "/create/vm" },
  { label: "LXC container", icon: "lxc", href: "/create/lxc" },
  { label: "Nodes", icon: "node", href: "/nodes" },
  { label: "Storage", icon: "vol", href: "/storage" },
  { label: "See the catalog", icon: "plus", href: "/create" },
];

export function ServiceTiles() {
  return (
    <section>
      <h2 className="mt-[26px] mb-[10px] text-[14px] font-semibold">Proxcloud services</h2>
      <div className="flex flex-wrap gap-2">
        {TILES.map((t) => (
          <Link
            key={t.label}
            href={t.href}
            className="flex h-24 w-[104px] cursor-pointer flex-col items-center justify-center gap-[10px] rounded-fluent border border-line bg-card shadow-card hover:shadow-card-hover hover:no-underline"
          >
            {t.icon === "plus" ? (
              <Mi name="plus" size={24} color="var(--color-accent)" strokeWidth={1.2} />
            ) : (
              <Svc name={t.icon} size={26} />
            )}
            <span className="text-center text-[12px] leading-[1.3] text-ink">{t.label}</span>
          </Link>
        ))}
      </div>
    </section>
  );
}

// ── §3.1 recent resources ────────────────────────────────────────────────────

export function RecentResourcesCard() {
  const resources = useResources();
  const recents = listRecent();

  // Only show entries that still exist on the server — never a stale ghost.
  const live = new Map((resources.data ?? []).map((g) => [g.id, g]));
  const rows = recents
    .filter((r) => live.has(r.id))
    .map((r) => ({ ...r, guest: live.get(r.id) as GuestSummary }));

  return (
    <Card>
      <div className="flex items-center justify-between px-4 pt-[14px] pb-[10px]">
        <h3 className="text-[14px] font-semibold">Recent resources</h3>
        <Link href="/resources" className="text-[13px]">
          See all resources
        </Link>
      </div>
      {resources.isPending ? (
        <div className="space-y-2 px-4 pb-4">
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
        </div>
      ) : resources.isError ? (
        <div className="px-4 pb-3">
          <CardError err={resources.error} />
        </div>
      ) : rows.length === 0 ? (
        <p className="px-4 pb-5 text-[13px] text-ink-2">
          No recently viewed resources yet — open one from All resources.
        </p>
      ) : (
        <table className="w-full border-collapse text-[13px]">
          <thead>
            <tr>
              {["Name", "Type", "Node", "Last viewed"].map((h) => (
                <th key={h} className="border-b border-line px-4 py-[6px] text-left font-semibold">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.id} className="border-b border-line-row">
                <td className="h-10 px-4">
                  <Link
                    href={`/resources/${r.node}/${r.id}`}
                    className="inline-flex items-center gap-2"
                  >
                    <Svc name={r.type === "qemu" ? "vm" : "lxc"} size={18} />
                    {r.name}
                  </Link>
                </td>
                <td className="px-4 text-ink-2">{r.type === "qemu" ? "Virtual machine" : "LXC container"}</td>
                <td className="px-4 text-ink-2">{r.node}</td>
                <td className="px-4 text-ink-2 tabular-nums">{relativeTime(r.viewedAt)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

// ── nodes card (design chart-tile anatomy §3.5.1) ────────────────────────────

function NodeRow({ node }: { node: string }) {
  const metrics = useNodeMetrics(node);
  const cpu = metrics.data?.series?.cpu ?? [];
  return cpu.length > 0 ? (
    <Sparkline points={cpu.map((p) => ({ t: p.t, v: p.v }))} height={48} max={100} />
  ) : metrics.isPending ? (
    <Skeleton className="h-12" />
  ) : (
    <p className="text-[12px] text-ink-3">No metric data</p>
  );
}

export function NodesCard() {
  const nodes = useNodes();
  return (
    <Card className="p-4">
      <h3 className="mb-3 text-[14px] font-semibold">Nodes</h3>
      {nodes.isPending ? (
        <Skeleton className="h-24" />
      ) : nodes.isError ? (
        <CardError err={nodes.error} />
      ) : (
        <div className="grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-3">
          {nodes.data.map((n) => (
            <div key={n.node} className="rounded-fluent border border-line px-[14px] py-3">
              <div className="flex items-center justify-between text-[13px]">
                <Link href={`/nodes/${n.node}`} className="font-semibold">
                  {n.node}
                </Link>
                <StatusDot status={n.online ? "online" : "offline"} label={n.online ? "Online" : "Offline"} />
              </div>
              <div className="mt-[2px] text-[12px] text-ink-2">
                {n.pveVersion || "—"} · up {formatUptime(n.uptimeSec)}
              </div>
              <div className="mt-2 flex items-center justify-between text-[12px]">
                <span className="text-ink-2">CPU</span>
                <span className="font-semibold tabular-nums">{formatPct(n.cpuPct, 1)}</span>
              </div>
              <div className="mt-1">
                <NodeRow node={n.node} />
              </div>
              <div className="mt-2 flex items-center justify-between text-[12px]">
                <span className="text-ink-2">RAM</span>
                <span className="tabular-nums text-ink-2">{formatBytesPair(n.memUsed, n.memTotal)}</span>
              </div>
              <ProgressBar pct={n.memTotal > 0 ? (n.memUsed / n.memTotal) * 100 : 0} className="mt-1" />
            </div>
          ))}
        </div>
      )}
    </Card>
  );
}

// ── right rail: usage / guests / service health ──────────────────────────────

export function UsageCard() {
  const cluster = useCluster();
  if (cluster.isPending)
    return (
      <Card className="p-4">
        <Skeleton className="h-28" />
      </Card>
    );
  if (cluster.isError)
    return (
      <Card className="p-4">
        <CardError err={cluster.error} />
      </Card>
    );
  const u = cluster.data.usage;
  const rows = [
    { label: "CPU", value: formatPct(u.cpuPct, 1), pct: u.cpuPct },
    {
      label: "RAM",
      value: formatBytesPair(u.memUsed, u.memTotal),
      pct: u.memTotal > 0 ? (u.memUsed / u.memTotal) * 100 : 0,
    },
    {
      label: "Storage",
      value: formatBytesPair(u.diskUsed, u.diskTotal),
      pct: u.diskTotal > 0 ? (u.diskUsed / u.diskTotal) * 100 : 0,
    },
  ];
  return (
    <Card className="p-4">
      <h3 className="mb-3 text-[14px] font-semibold">Usage — {cluster.data.name || "cluster"}</h3>
      {rows.map((r) => (
        <div key={r.label} className="mb-3 last:mb-0">
          <div className="flex items-center justify-between text-[13px]">
            <span>{r.label}</span>
            <span className="tabular-nums text-ink-2">{r.value}</span>
          </div>
          <ProgressBar pct={r.pct} className="mt-1" />
        </div>
      ))}
    </Card>
  );
}

export function GuestsCard() {
  const cluster = useCluster();
  if (cluster.isPending)
    return (
      <Card className="p-4">
        <Skeleton className="h-20" />
      </Card>
    );
  if (cluster.isError)
    return (
      <Card className="p-4">
        <CardError err={cluster.error} />
      </Card>
    );
  const g = cluster.data.guests;
  const rows = [
    { icon: "vm" as const, label: "Virtual machines", running: g.vmsRunning, total: g.vmsTotal, href: "/resources?type=qemu" },
    { icon: "lxc" as const, label: "LXC containers", running: g.lxcsRunning, total: g.lxcsTotal, href: "/resources?type=lxc" },
  ];
  return (
    <Card className="p-4">
      <h3 className="mb-3 text-[14px] font-semibold">Guests</h3>
      {rows.map((r) => (
        <Link
          key={r.label}
          href={r.href}
          className="mb-2 flex items-center gap-[10px] text-ink last:mb-0 hover:no-underline"
        >
          <Svc name={r.icon} size={26} />
          <span className="flex-1 text-[13px]">{r.label}</span>
          <StatusDot
            status={r.running > 0 ? "running" : "stopped"}
            label={`${r.running} of ${r.total} running`}
          />
        </Link>
      ))}
    </Card>
  );
}

export function ServiceHealthCard() {
  const nodes = useNodes();
  const health = useHealth();
  return (
    <Card className="p-4">
      <h3 className="mb-[10px] text-[14px] font-semibold">Service health</h3>
      {nodes.isPending ? (
        <Skeleton className="h-10" />
      ) : nodes.isError ? (
        <CardError err={nodes.error} />
      ) : (
        <div className="flex flex-wrap gap-x-4 gap-y-2 text-[12px] text-ink-2">
          {nodes.data.map((n) => (
            <StatusDot key={n.node} status={n.online ? "online" : "offline"} label={n.node} />
          ))}
          <StatusDot
            status={health.data?.proxmox === "ok" ? "online" : "offline"}
            label="Proxmox API"
          />
        </div>
      )}
    </Card>
  );
}
