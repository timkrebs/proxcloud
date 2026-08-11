"use client";
// Activity log — design §3.7: the real cluster task list with live status;
// clicking a row opens the task-log flyout with the verbatim Proxmox log.
import { useState } from "react";
import Link from "next/link";
import { useQuery } from "@tanstack/react-query";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { Card } from "@/components/ui/Card";
import { Flyout } from "@/components/ui/Flyout";
import { StatusDot } from "@/components/ui/StatusDot";
import { apiFetch } from "@/lib/api/client";
import type { TaskLog, TaskSummary } from "@/lib/api/generated/types";
import { useCluster, useTasks } from "@/lib/api/queries";
import { formatDateTime, relativeTime } from "@/lib/format";
import { statusLabel } from "@/lib/status";

function TaskLogFlyout({ task, onClose }: { task: TaskSummary; onClose: () => void }) {
  const log = useQuery({
    queryKey: ["task", task.upid, "log"],
    queryFn: () => apiFetch<TaskLog>(`/api/tasks/${encodeURIComponent(task.upid)}/log?limit=200`),
    refetchInterval: task.status === "running" ? 2000 : false,
  });

  return (
    <Flyout title={task.action} width={440} onClose={onClose}>
      <div className="mb-3 text-[13px]">
        <div className="flex py-[2px]">
          <span className="w-[110px] flex-none text-ink-2">Status</span>
          <StatusDot status={task.status} label={statusLabel(task.status)} />
        </div>
        <div className="flex py-[2px]">
          <span className="w-[110px] flex-none text-ink-2">Started</span>
          <span className="tabular-nums">{formatDateTime(task.startedAt)}</span>
        </div>
        {task.exitStatus ? (
          <div className="flex py-[2px]">
            <span className="w-[110px] flex-none text-ink-2">Exit status</span>
            <span className={task.status === "failed" ? "text-err-text" : ""}>{task.exitStatus}</span>
          </div>
        ) : null}
        <div className="flex py-[2px]">
          <span className="w-[110px] flex-none text-ink-2">UPID</span>
          <span className="font-mono text-[11px] break-all">{task.upid}</span>
        </div>
      </div>
      {log.isPending ? (
        <Skeleton className="h-40" />
      ) : log.isError ? (
        <CardError err={log.error} />
      ) : (
        <pre className="rounded-fluent border border-line bg-hover p-3 font-mono text-[12px] leading-[1.5] whitespace-pre-wrap">
          {log.data.lines.length > 0 ? log.data.lines.map((l) => l.t).join("\n") : "(empty log)"}
        </pre>
      )}
    </Flyout>
  );
}

export default function ActivityPage() {
  const tasks = useTasks({});
  const cluster = useCluster();
  const [open, setOpen] = useState<TaskSummary | null>(null);

  return (
    <div className="max-w-[1360px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">Activity log</span>
      </nav>
      <h1 className="mb-1 text-[24px] font-semibold">Activity log</h1>
      <p className="mb-3 text-[12px] text-ink-2">
        All control-plane operations on {cluster.data?.name || "this cluster"} — Proxmox&apos;s own task
        history, live.
      </p>

      <Card>
        {tasks.isPending ? (
          <div className="space-y-2 p-4">
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
          </div>
        ) : tasks.isError ? (
          <div className="p-4">
            <CardError err={tasks.error} />
          </div>
        ) : (tasks.data ?? []).length === 0 ? (
          <p className="p-6 text-[13px] text-ink-2">No recorded tasks.</p>
        ) : (
          <table className="w-full border-collapse text-[13px]">
            <thead>
              <tr>
                {["Operation", "Resource", "Initiated by", "Status", "Time"].map((h) => (
                  <th key={h} className="border-b border-line bg-hover px-4 py-2 text-left font-semibold">
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {(tasks.data ?? []).map((t) => (
                <tr
                  key={t.upid}
                  className="cursor-pointer border-b border-line-row last:border-b-0 hover:bg-hover"
                  onClick={() => setOpen(t)}
                >
                  <td className="h-10 px-4">{t.action}</td>
                  <td className="px-4">
                    {t.resource ? (
                      <Link
                        href={`/resources/${t.resource.node}/${t.resource.type}/${t.resource.vmid}`}
                        onClick={(e) => e.stopPropagation()}
                      >
                        {t.resource.name || `VMID ${t.resource.vmid}`}
                      </Link>
                    ) : (
                      <span className="text-ink-2">—</span>
                    )}
                  </td>
                  <td className="px-4 text-ink-2">{t.user}</td>
                  <td className="px-4">
                    <StatusDot
                      status={t.status === "running" ? "in progress" : t.status}
                      label={t.status === "running" ? "In progress" : statusLabel(t.status)}
                    />
                  </td>
                  <td className="px-4 text-ink-2 tabular-nums">{relativeTime(t.startedAt)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Card>

      {open ? <TaskLogFlyout task={open} onClose={() => setOpen(null)} /> : null}
    </div>
  );
}
