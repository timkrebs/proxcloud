"use client";
// Guest activity blade — design §3.5.2: real tasks scoped to this VMID.
import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { BladeHeading, BladeTable, bladeCell, bladeCellMuted, useGuestParams } from "@/components/guest/common";
import { StatusDot } from "@/components/ui/StatusDot";
import { useTasks } from "@/lib/api/queries";
import { relativeTime } from "@/lib/format";
import { statusLabel } from "@/lib/status";

export default function GuestActivityPage() {
  const g = useGuestParams();
  const tasks = useTasks({ vmid: g.vmid });

  return (
    <div>
      <BladeHeading>Activity log</BladeHeading>
      {tasks.isPending ? (
        <Skeleton className="h-32" />
      ) : tasks.isError ? (
        <CardError err={tasks.error} />
      ) : (tasks.data ?? []).length === 0 ? (
        <p className="text-[13px] text-ink-2">No recorded tasks for this guest.</p>
      ) : (
        <BladeTable headers={["Operation", "Initiated by", "Status", "Time"]}>
          {(tasks.data ?? []).map((t) => (
            <tr key={t.upid} className="border-b border-line-row last:border-b-0">
              <td className={bladeCell}>{t.action}</td>
              <td className={bladeCellMuted}>{t.user}</td>
              <td className={bladeCell}>
                <StatusDot status={t.status} label={statusLabel(t.status)} />
              </td>
              <td className={`${bladeCellMuted} tabular-nums`}>{relativeTime(t.startedAt)}</td>
            </tr>
          ))}
        </BladeTable>
      )}
    </div>
  );
}
