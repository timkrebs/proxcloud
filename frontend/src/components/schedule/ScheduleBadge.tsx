"use client";
// Schedule chip: the human summary ("Auto-shutdown 22:00 · Mon–Fri") plus a
// live-ticking next-shutdown countdown computed client-side from the schedule's
// shutdownTime + daysOfWeek + timezone. A paused/never-firing schedule shows the
// summary without a countdown.
import { Mi } from "@/components/ui/icons";
import type { Schedule } from "@/lib/api/generated/types";
import { formatCountdown } from "@/lib/format";
import { nextRun, scheduleSummary } from "@/lib/schedule";
import { useNow } from "@/lib/useCountdown";

export function ScheduleBadge({ schedule }: { schedule: Schedule }) {
  const now = useNow(30_000);
  const summary = scheduleSummary(schedule);
  const next = schedule.enabled
    ? nextRun(schedule.shutdownTime, schedule.daysOfWeek, schedule.timezone, now)
    : null;

  return (
    <span className="inline-flex flex-wrap items-center gap-[6px]">
      <span className="inline-flex items-center gap-1 rounded-fluent border border-line bg-hover px-2 py-[2px] text-[11px] text-ink-2">
        <Mi name="clock" size={11} color="currentColor" />
        {schedule.enabled ? summary : `${summary} · paused`}
      </span>
      {next ? (
        <span
          className="inline-flex items-center rounded-fluent border border-line bg-card px-2 py-[2px] text-[11px] tabular-nums text-ink-2"
          title={next.toLocaleString()}
        >
          {formatCountdown(next.toISOString(), now)}
        </span>
      ) : null}
    </span>
  );
}
