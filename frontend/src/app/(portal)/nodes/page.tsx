"use client";
// Nodes — real per-node health, capacity, and version.
import Link from "next/link";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { Card } from "@/components/ui/Card";
import { ProgressBar } from "@/components/ui/ProgressBar";
import { StatusDot } from "@/components/ui/StatusDot";
import { Svc } from "@/components/ui/icons";
import { useNodes } from "@/lib/api/queries";
import { formatBytesPair, formatPct, formatUptime } from "@/lib/format";

export default function NodesPage() {
  const nodes = useNodes();

  return (
    <div className="max-w-[1360px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">Nodes</span>
      </nav>
      <h1 className="mb-3 text-[24px] font-semibold">Nodes</h1>

      {nodes.isPending ? (
        <Skeleton className="h-40" />
      ) : nodes.isError ? (
        <CardError err={nodes.error} />
      ) : (
        <div className="grid grid-cols-[repeat(auto-fill,minmax(320px,1fr))] gap-4">
          {nodes.data.map((n) => (
            <Card key={n.node} hoverable className="p-4">
              <Link href={`/nodes/${n.node}`} className="flex items-center gap-2 text-ink hover:no-underline">
                <Svc name="node" size={24} />
                <span className="text-[16px] font-semibold">{n.node}</span>
                <span className="ml-auto">
                  <StatusDot status={n.online ? "online" : "offline"} label={n.online ? "Online" : "Offline"} />
                </span>
              </Link>
              <p className="mt-1 text-[12px] text-ink-2">
                {n.pveVersion || "version unknown"} · up {formatUptime(n.uptimeSec)}
              </p>
              <div className="mt-3 space-y-3 text-[13px]">
                <div>
                  <div className="flex justify-between">
                    <span>CPU</span>
                    <span className="tabular-nums text-ink-2">{formatPct(n.cpuPct, 1)}</span>
                  </div>
                  <ProgressBar pct={n.cpuPct} className="mt-1" />
                </div>
                <div>
                  <div className="flex justify-between">
                    <span>RAM</span>
                    <span className="tabular-nums text-ink-2">{formatBytesPair(n.memUsed, n.memTotal)}</span>
                  </div>
                  <ProgressBar pct={n.memTotal > 0 ? (n.memUsed / n.memTotal) * 100 : 0} className="mt-1" />
                </div>
                <div>
                  <div className="flex justify-between">
                    <span>Root FS</span>
                    <span className="tabular-nums text-ink-2">{formatBytesPair(n.diskUsed, n.diskTotal)}</span>
                  </div>
                  <ProgressBar pct={n.diskTotal > 0 ? (n.diskUsed / n.diskTotal) * 100 : 0} className="mt-1" />
                </div>
              </div>
            </Card>
          ))}
        </div>
      )}
    </div>
  );
}
