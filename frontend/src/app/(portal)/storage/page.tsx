"use client";
// Storage — every pool the cluster reports, with usage and content types.
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { Card } from "@/components/ui/Card";
import { ProgressBar } from "@/components/ui/ProgressBar";
import { StatusDot } from "@/components/ui/StatusDot";
import { apiFetch } from "@/lib/api/client";
import type { StorageSummary } from "@/lib/api/generated/types";
import { formatBytesPair } from "@/lib/format";

export default function StoragePage() {
  const storage = useQuery({
    queryKey: ["storage"],
    queryFn: () => apiFetch<StorageSummary[]>("/api/storage"),
    refetchInterval: 30_000,
  });

  return (
    <div className="max-w-[1360px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">Storage</span>
      </nav>
      <h1 className="mb-3 text-[24px] font-semibold">Storage</h1>

      <Card>
        {storage.isPending ? (
          <div className="space-y-2 p-4">
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
          </div>
        ) : storage.isError ? (
          <div className="p-4">
            <CardError err={storage.error} />
          </div>
        ) : (
          <table className="w-full border-collapse text-[13px]">
            <thead>
              <tr>
                {["Name", "Node", "Type", "Content", "Status", "Usage"].map((h) => (
                  <th key={h} className="border-b border-line bg-hover px-4 py-2 text-left font-semibold">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {(storage.data ?? []).map((s) => (
                <tr key={`${s.node}/${s.storage}`} className="border-b border-line-row last:border-b-0">
                  <td className="h-10 px-4 font-semibold">
                    {s.storage}
                    {s.shared ? <span className="ml-2 text-[11px] font-normal text-ink-3">shared</span> : null}
                  </td>
                  <td className="px-4 text-ink-2">{s.node}</td>
                  <td className="px-4 text-ink-2">{s.type}</td>
                  <td className="px-4 text-ink-2">{s.content.join(", ")}</td>
                  <td className="px-4">
                    <StatusDot status={s.active ? "active" : "offline"} label={s.active ? "Active" : "Inactive"} />
                  </td>
                  <td className="w-[280px] px-4">
                    <div className="flex items-center gap-2">
                      <ProgressBar pct={s.total > 0 ? (s.used / s.total) * 100 : 0} className="flex-1" />
                      <span className="text-[12px] text-ink-2 tabular-nums">{formatBytesPair(s.used, s.total)}</span>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </div>
  );
}
