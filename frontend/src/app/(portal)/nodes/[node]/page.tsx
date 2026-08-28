"use client";
// Node detail — live hardware detail + hour-scale metric charts.
import Link from "next/link";
import { useParams } from "next/navigation";
import { useQuery } from "@tanstack/react-query";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { AdminOnly } from "@/components/chrome/AdminOnly";
import { Card } from "@/components/ui/Card";
import { Sparkline } from "@/components/ui/Sparkline";
import { apiFetch } from "@/lib/api/client";
import type { NodeDetail } from "@/lib/api/generated/types";
import { useMe, useNodeMetrics } from "@/lib/api/queries";
import { formatBytes, formatBytesPair, formatPct, formatRate, formatUptime } from "@/lib/format";

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex py-[3px] text-[13px]">
      <span className="w-[160px] flex-none text-ink-2">{k}</span>
      <span>{v}</span>
    </div>
  );
}

export default function NodeDetailPage() {
  const { node } = useParams<{ node: string }>();
  const me = useMe();
  const isAdmin = !!me.data?.isPlatformAdmin;
  const detail = useQuery({
    queryKey: ["node", node],
    queryFn: () => apiFetch<NodeDetail>(`/api/admin/nodes/${encodeURIComponent(node)}`),
    refetchInterval: 10_000,
    enabled: isAdmin,
  });
  const metrics = useNodeMetrics(node);
  const s = metrics.data?.series ?? {};
  const last = (key: string) => {
    const arr = s[key] ?? [];
    return arr.length > 0 ? arr[arr.length - 1].v : undefined;
  };

  const charts = [
    {
      label: "CPU",
      value: last("cpu") !== undefined ? formatPct(last("cpu")!, 1) : "—",
      points: s.cpu ?? [],
      max: 100,
    },
    {
      label: "IO wait",
      value: last("iowait") !== undefined ? formatPct(last("iowait")!, 1) : "—",
      points: s.iowait ?? [],
      max: 100,
    },
    {
      label: "Memory",
      value: last("memused") !== undefined ? formatBytes(last("memused")!) : "—",
      points: s.memused ?? [],
      max: last("memtotal"),
    },
    {
      label: "Network (in/out)",
      value:
        last("netin") !== undefined
          ? `${formatRate(last("netin")!)} / ${formatRate(last("netout") ?? 0)}`
          : "—",
      points: (s.netin ?? []).map((p, i) => ({ t: p.t, v: p.v + (s.netout?.[i]?.v ?? 0) })),
      max: undefined,
    },
  ];

  if (me.data && !isAdmin) return <AdminOnly resource="Nodes" />;

  return (
    <div className="max-w-[1100px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <Link href="/nodes">Nodes</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">{node}</span>
      </nav>
      <h1 className="mb-3 text-[24px] font-semibold">{node}</h1>

      {detail.isPending ? (
        <Skeleton className="h-40" />
      ) : detail.isError ? (
        <CardError err={detail.error} />
      ) : (
        <Card className="mb-4 max-w-[640px] px-4 py-3">
          <Row k="PVE version" v={detail.data.pveVersion || "—"} />
          <Row k="Kernel" v={detail.data.kernelVersion || "—"} />
          <Row
            k="CPU"
            v={`${detail.data.cpuModel || "—"} (${detail.data.cpuCores} cores, ${detail.data.cpuSockets} socket${detail.data.cpuSockets === 1 ? "" : "s"})`}
          />
          <Row
            k="Load average"
            v={(detail.data.loadAvg ?? []).map((l) => l.toFixed(2)).join(" / ") || "—"}
          />
          <Row k="Memory" v={formatBytesPair(detail.data.memUsed, detail.data.memTotal)} />
          <Row
            k="Swap"
            v={
              detail.data.swapTotal > 0
                ? formatBytesPair(detail.data.swapUsed, detail.data.swapTotal)
                : "none"
            }
          />
          <Row k="Root FS" v={formatBytesPair(detail.data.diskUsed, detail.data.diskTotal)} />
          <Row k="Uptime" v={formatUptime(detail.data.uptimeSec)} />
        </Card>
      )}

      <h2 className="mb-3 text-[16px] font-semibold">
        Metrics{" "}
        <span className="text-[12px] font-normal text-ink-2">
          · Last hour · 1-minute granularity
        </span>
      </h2>
      <div className="grid grid-cols-[repeat(auto-fit,minmax(280px,1fr))] gap-3">
        {charts.map((c) => (
          <Card key={c.label} className="px-4 py-[14px]">
            <div className="flex items-center justify-between text-[13px]">
              <span className="text-ink-2">{c.label}</span>
              <span className="font-semibold tabular-nums">{c.value}</span>
            </div>
            <div className="mt-[10px]">
              {c.points.length > 0 ? (
                <Sparkline points={c.points} height={90} max={c.max} />
              ) : metrics.isPending ? (
                <Skeleton className="h-[90px]" />
              ) : (
                <p className="py-8 text-center text-[12px] text-ink-3">No metric data</p>
              )}
            </div>
          </Card>
        ))}
      </div>
    </div>
  );
}
