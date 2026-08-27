"use client";
// Metrics blade — design §3.5.9: four 90px charts, real rrddata series.
import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { BladeHeading, useGuestParams } from "@/components/guest/common";
import { Card } from "@/components/ui/Card";
import { Sparkline } from "@/components/ui/Sparkline";
import { useGuest, useGuestMetrics } from "@/lib/api/guestQueries";
import { formatPct, formatRate } from "@/lib/format";

export default function GuestMetricsPage() {
  const g = useGuestParams();
  const guest = useGuest(g);
  const metrics = useGuestMetrics(g);

  if (metrics.isPending || guest.isPending) return <Skeleton className="h-64" />;
  if (metrics.isError) return <CardError err={metrics.error} />;
  const s = metrics.data.series;
  const memMax = guest.data?.memMax ?? 0;

  const last = (key: string) => {
    const arr = s[key] ?? [];
    return arr.length > 0 ? arr[arr.length - 1].v : undefined;
  };
  const combined = (a: string, b: string) =>
    (s[a] ?? []).map((p, i) => ({ t: p.t, v: p.v + (s[b]?.[i]?.v ?? 0) }));

  const charts = [
    {
      label: "CPU (average)",
      value: last("cpu") !== undefined ? formatPct(last("cpu")!, 0) : "—",
      points: s.cpu ?? [],
      max: 100,
    },
    {
      label: "Memory used",
      value:
        last("mem") !== undefined && memMax > 0 ? formatPct((last("mem")! / memMax) * 100, 0) : "—",
      points: s.mem ?? [],
      max: memMax || undefined,
    },
    {
      label: "Disk I/O (read/write)",
      value:
        last("diskread") !== undefined
          ? `${formatRate(last("diskread")!)} / ${formatRate(last("diskwrite") ?? 0)}`
          : "—",
      points: combined("diskread", "diskwrite"),
      max: undefined,
    },
    {
      label: "Network (in/out)",
      value:
        last("netin") !== undefined
          ? `${formatRate(last("netin")!)} / ${formatRate(last("netout") ?? 0)}`
          : "—",
      points: combined("netin", "netout"),
      max: undefined,
    },
  ];

  const stopped = guest.data?.status !== "running";

  return (
    <div>
      <BladeHeading sub="Last hour · 1-minute granularity">Metrics</BladeHeading>
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
              ) : (
                <p className="py-8 text-center text-[12px] text-ink-3">
                  {stopped ? "No data — guest stopped" : "No metric data"}
                </p>
              )}
            </div>
          </Card>
        ))}
      </div>
    </div>
  );
}
