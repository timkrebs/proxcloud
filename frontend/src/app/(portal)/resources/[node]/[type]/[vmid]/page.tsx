"use client";
// Overview blade — design §3.5.1: collapsible Essentials grid with copy
// buttons + JSON view link, three sparkline tiles, recent guest tasks.
import { useState } from "react";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { useGuestParams } from "@/components/guest/common";
import { Card } from "@/components/ui/Card";
import { Sparkline } from "@/components/ui/Sparkline";
import { StatusDot } from "@/components/ui/StatusDot";
import { Mi } from "@/components/ui/icons";
import { useGuest, useGuestInterfaces, useGuestMetrics } from "@/lib/api/guestQueries";
import { useTasks } from "@/lib/api/queries";
import { formatBytes, formatBytesPair, formatPct, formatRate, formatUptime, relativeTime } from "@/lib/format";
import { statusLabel } from "@/lib/status";
import { pushToast } from "@/lib/stores/toastStore";

function EssRow({ k, v, copy }: { k: string; v: React.ReactNode; copy?: string }) {
  return (
    <div className="flex py-[3px] text-[13px]">
      <span className="w-[130px] flex-none text-ink-2">{k}</span>
      <span className="flex items-center gap-1">
        {v}
        {copy ? (
          <button
            type="button"
            title="Copy"
            className="cursor-pointer border-none bg-transparent p-[2px] text-ink-2 hover:text-accent"
            onClick={() => {
              navigator.clipboard.writeText(copy);
              pushToast({ kind: "info", title: "Copied to clipboard", desc: copy });
            }}
          >
            <Mi name="copy" size={12} color="currentColor" />
          </button>
        ) : null}
      </span>
    </div>
  );
}

export default function OverviewPage() {
  const g = useGuestParams();
  const guest = useGuest(g);
  const metrics = useGuestMetrics(g);
  const interfaces = useGuestInterfaces(g, guest.data?.status === "running");
  const tasks = useTasks({ vmid: g.vmid });
  const [essOpen, setEssOpen] = useState(true);

  if (guest.isPending) return <Skeleton className="h-64" />;
  if (guest.isError) return <CardError err={guest.error} />;
  const d = guest.data;

  const ip4 = (interfaces.data?.nics ?? []).flatMap((n) => n.ipv4).filter((a) => !a.startsWith("127."));
  const series = metrics.data?.series ?? {};
  const last = (key: string) => {
    const s = series[key] ?? [];
    return s.length > 0 ? s[s.length - 1].v : undefined;
  };
  const netSeries = (series.netin ?? []).map((p, i) => ({
    t: p.t,
    v: p.v + (series.netout?.[i]?.v ?? 0),
  }));

  const tiles = [
    { label: "CPU (average)", value: last("cpu") !== undefined ? formatPct(last("cpu")!, 0) : "—", points: series.cpu ?? [], max: 100 },
    {
      label: "Memory used",
      value: last("mem") !== undefined && d.memMax > 0 ? formatPct((last("mem")! / d.memMax) * 100, 0) : "—",
      points: (series.mem ?? []).map((p) => ({ t: p.t, v: p.v })),
      max: d.memMax,
    },
    {
      label: "Network (in/out)",
      value:
        last("netin") !== undefined
          ? `${formatRate(last("netin")!)} / ${formatRate(last("netout") ?? 0)}`
          : "—",
      points: netSeries,
      max: undefined,
    },
  ];

  return (
    <div>
      {/* Essentials */}
      <div className="border-b border-line pb-[14px]">
        <div className="flex items-center justify-between">
          <button
            type="button"
            onClick={() => setEssOpen((o) => !o)}
            className="flex cursor-pointer items-center gap-2 border-none bg-transparent text-[14px] font-semibold text-ink"
          >
            <Mi
              name="chevronDown"
              size={12}
              style={{ transform: essOpen ? "rotate(0deg)" : "rotate(-90deg)", transition: "transform .15s" }}
            />
            Essentials
          </button>
          <a
            href="#"
            className="text-[13px]"
            onClick={(e) => {
              e.preventDefault();
              window.dispatchEvent(new Event("proxcloud:open-json"));
            }}
          >
            JSON view
          </a>
        </div>
        {essOpen ? (
          <div className="mt-3 grid grid-cols-2 gap-x-10">
            <div>
              <EssRow k="Status" v={<StatusDot status={d.status} label={statusLabel(d.status)} />} />
              <EssRow k="Node" v={d.node} />
              <EssRow k="Cores" v={String(d.cores)} />
              <EssRow k="Memory" v={d.status === "running" ? formatBytesPair(d.memUsed, d.memMax) : formatBytes(d.memMax)} />
              <EssRow k="Uptime" v={d.status === "running" ? formatUptime(d.uptimeSec) : "—"} />
            </div>
            <div>
              <EssRow
                k="IP addresses"
                v={
                  d.status !== "running" ? (
                    "— (stopped)"
                  ) : interfaces.data?.agentUnavailable ? (
                    <span className="text-ink-2">Guest agent not running — install qemu-guest-agent</span>
                  ) : ip4.length > 0 ? (
                    ip4.join(", ")
                  ) : interfaces.isPending ? (
                    "…"
                  ) : (
                    "none reported"
                  )
                }
                copy={ip4[0]}
              />
              <EssRow k="OS type" v={d.osType || "—"} />
              <EssRow k="Boot disk" v={d.bootDisk || (d.type === "lxc" ? "rootfs" : "—")} />
              <EssRow k="Start on boot" v={d.onBoot ? "Yes" : "No"} />
              <EssRow
                k="Tags"
                v={
                  d.tags.length > 0
                    ? d.tags.map((t) => (
                        <span key={t} className="mr-[6px] rounded-fluent border border-line bg-hover px-2 py-[2px] text-[11px]">
                          {t}
                        </span>
                      ))
                    : "—"
                }
              />
            </div>
          </div>
        ) : null}
      </div>

      {/* sparkline tiles */}
      <div className="mt-[18px] grid grid-cols-[repeat(auto-fit,minmax(220px,1fr))] gap-3">
        {tiles.map((tile) => (
          <Card key={tile.label} className="px-[14px] py-3">
            <div className="flex items-center justify-between text-[12px]">
              <span className="text-ink-2">{tile.label}</span>
              <span className="font-semibold tabular-nums">{tile.value}</span>
            </div>
            <div className="mt-2">
              {tile.points.length > 0 ? (
                <Sparkline points={tile.points} height={48} max={tile.max} />
              ) : d.status !== "running" ? (
                <p className="py-4 text-[12px] text-ink-3">No data — guest stopped</p>
              ) : metrics.isPending ? (
                <Skeleton className="h-12" />
              ) : (
                <p className="py-4 text-[12px] text-ink-3">No metric data</p>
              )}
            </div>
          </Card>
        ))}
      </div>

      {/* recent tasks on this guest */}
      <h3 className="mt-[22px] mb-2 text-[14px] font-semibold">Recent activity on this resource</h3>
      <Card>
        {tasks.isPending ? (
          <Skeleton className="m-3 h-16" />
        ) : tasks.isError ? (
          <div className="p-3">
            <CardError err={tasks.error} />
          </div>
        ) : (tasks.data ?? []).length === 0 ? (
          <p className="p-4 text-[13px] text-ink-2">No recorded tasks for this guest.</p>
        ) : (
          (tasks.data ?? []).slice(0, 6).map((t) => (
            <div key={t.upid} className="flex items-center gap-[10px] border-b border-line-row px-[14px] py-[9px] text-[13px] last:border-b-0">
              <StatusDot status={t.status} />
              <span className="flex-1">{t.action}</span>
              <span className="text-ink-2">{t.user}</span>
              <span className="w-20 text-right text-ink-2">{relativeTime(t.startedAt)}</span>
            </div>
          ))
        )}
      </Card>
    </div>
  );
}
