"use client";
// Activity log (Phase 4) — the merged timeline: Proxcloud's own audit feed
// (who did what, with intent+outcome) overlaid with the raw Proxmox task feed.
// Keyset "Load more" walks older audit rows via ?before=nextBefore. Filters by
// source (Proxcloud vs Proxmox), project, and outcome. Every value is the
// backend's — loading / empty / error+retry per DoD.
import { useState } from "react";
import Link from "next/link";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { Select } from "@/components/ui/Select";
import { Mi } from "@/components/ui/icons";
import type { ActivityEntry } from "@/lib/api/generated/types";
import { useActivity } from "@/lib/api/quota";
import { useProjects } from "@/lib/api/tenant";
import type { ActivityFilters } from "@/lib/api/queryKeys";
import { actionLabel, sourceLabel, targetLabel } from "@/lib/activity";
import { formatDateTime, relativeTime } from "@/lib/format";

// Outcome → dot color, keyed to design status tokens (referenced as CSS vars,
// not raw hex). audit: pending|success|denied|error · task: running|succeeded|failed
const OUTCOME_TONE: Record<string, string> = {
  success: "var(--color-ok)",
  succeeded: "var(--color-ok)",
  error: "var(--color-err)",
  failed: "var(--color-err)",
  denied: "var(--color-warn)",
  pending: "var(--color-ink-3)",
  running: "var(--color-accent)",
};

function OutcomeChip({ outcome }: { outcome: string }) {
  const tone = OUTCOME_TONE[outcome] ?? "var(--color-ink-3)";
  return (
    <span className="inline-flex items-center gap-[6px] rounded-fluent border border-line bg-card px-2 py-[2px] text-[12px] text-ink capitalize">
      <span className="h-2 w-2 shrink-0 rounded-full" style={{ background: tone }} aria-hidden />
      {outcome || "—"}
    </span>
  );
}

function SourceBadge({ source }: { source: string }) {
  const proxcloud = source === "audit";
  return (
    <span
      className={`inline-flex items-center rounded-fluent border px-[6px] py-[1px] text-[11px] ${
        proxcloud ? "border-accent bg-selected text-accent" : "border-line bg-hover text-ink-2"
      }`}
    >
      {sourceLabel(source)}
    </span>
  );
}

function ActivityRow({ entry }: { entry: ActivityEntry }) {
  return (
    <tr className="border-b border-line-row last:border-b-0">
      <td className="h-10 px-4">
        <SourceBadge source={entry.source} />
      </td>
      <td className="px-4">{actionLabel(entry.action)}</td>
      <td className="px-4 text-ink-2">
        {targetLabel(entry.targetType, entry.targetId, entry.projectName)}
      </td>
      <td className="px-4 text-ink-2">{entry.projectName || "—"}</td>
      <td className="px-4 text-ink-2">{entry.actor || "—"}</td>
      <td className="px-4">
        <OutcomeChip outcome={entry.outcome} />
      </td>
      <td className="px-4 text-ink-2 tabular-nums" title={formatDateTime(entry.ts)}>
        {relativeTime(entry.ts)}
      </td>
    </tr>
  );
}

const OUTCOMES = ["success", "denied", "error", "pending", "running", "succeeded", "failed"];

export default function ActivityPage() {
  const [filters, setFilters] = useState<ActivityFilters>({});
  const activity = useActivity(filters);
  const projects = useProjects();

  const entries = (activity.data?.pages ?? []).flatMap((p) => p.entries);

  const patch = (f: Partial<ActivityFilters>) => setFilters((prev) => ({ ...prev, ...f }));

  return (
    <div className="max-w-[1360px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">Activity log</span>
      </nav>
      <h1 className="mb-1 text-[24px] font-semibold">Activity log</h1>
      <p className="mb-3 text-[12px] text-ink-2">
        Every control-plane action on your resources — Proxcloud&apos;s own audit trail overlaid
        with Proxmox&apos;s live task history.
      </p>

      {/* Filters */}
      <div className="mb-3 flex flex-wrap items-center gap-3">
        <label className="flex items-center gap-2 text-[12px] text-ink-2">
          Source
          <Select
            value={filters.source ?? ""}
            aria-label="Filter by source"
            onChange={(e) =>
              patch({ source: (e.target.value || undefined) as ActivityFilters["source"] })
            }
          >
            <option value="">All</option>
            <option value="audit">Proxcloud</option>
            <option value="task">Proxmox</option>
          </Select>
        </label>

        <label className="flex items-center gap-2 text-[12px] text-ink-2">
          Project
          <Select
            value={filters.projectId ?? ""}
            aria-label="Filter by project"
            onChange={(e) => patch({ projectId: e.target.value || undefined })}
            disabled={projects.isPending || projects.isError}
          >
            <option value="">All projects</option>
            {(projects.data ?? []).map((p) => (
              <option key={p.id} value={p.id}>
                {p.name}
              </option>
            ))}
          </Select>
        </label>

        <label className="flex items-center gap-2 text-[12px] text-ink-2">
          Outcome
          <Select
            value={filters.outcome ?? ""}
            aria-label="Filter by outcome"
            onChange={(e) => patch({ outcome: e.target.value || undefined })}
          >
            <option value="">All</option>
            {OUTCOMES.map((o) => (
              <option key={o} value={o}>
                {o.charAt(0).toUpperCase() + o.slice(1)}
              </option>
            ))}
          </Select>
        </label>

        <button
          type="button"
          onClick={() => activity.refetch()}
          className="flex h-8 cursor-pointer items-center gap-[6px] border-none bg-transparent px-[10px] text-[13px] text-ink hover:bg-hover"
        >
          <Mi name="restart" size={14} />
          Refresh
        </button>
      </div>

      <Card>
        {activity.isPending ? (
          <div className="space-y-2 p-4">
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
          </div>
        ) : activity.isError ? (
          <div className="p-4">
            <CardError err={activity.error} />
            <div className="mt-3">
              <Button variant="secondaryCompact" onClick={() => activity.refetch()}>
                Retry
              </Button>
            </div>
          </div>
        ) : entries.length === 0 ? (
          <EmptyState
            icon="clock"
            title="No activity yet"
            body="Actions on your resources — creating a guest, editing a quota, running Proxmox tasks — will appear here as they happen."
          />
        ) : (
          <>
            <table className="w-full border-collapse text-[13px]">
              <thead>
                <tr>
                  {["Source", "Operation", "Target", "Project", "Actor", "Outcome", "Time"].map(
                    (h) => (
                      <th
                        key={h}
                        className="border-b border-line bg-hover px-4 py-2 text-left font-semibold"
                      >
                        {h}
                      </th>
                    ),
                  )}
                </tr>
              </thead>
              <tbody>
                {entries.map((e) => (
                  <ActivityRow key={e.id} entry={e} />
                ))}
              </tbody>
            </table>
            {activity.hasNextPage ? (
              <div className="flex justify-center border-t border-line px-4 py-3">
                <Button
                  variant="secondaryCompact"
                  disabled={activity.isFetchingNextPage}
                  onClick={() => activity.fetchNextPage()}
                >
                  {activity.isFetchingNextPage ? "Loading…" : "Load more"}
                </Button>
              </div>
            ) : null}
          </>
        )}
      </Card>
    </div>
  );
}
