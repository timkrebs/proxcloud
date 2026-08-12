"use client";
// Quota usage bars (Phase 4) — used-vs-limit per dimension (vCPU / Memory /
// Disk / Guests). A null limit renders "Unlimited" with no bar (the scope has
// no cap on that dimension); a set limit renders a bar that shifts warn→err as
// usage approaches/exceeds the cap. Design tokens only; no raw hex.
import type { ReactNode } from "react";

import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { ProgressBar, type ProgressTone } from "@/components/ui/ProgressBar";
import { Mi } from "@/components/ui/icons";
import { ApiError } from "@/lib/api/client";
import type { QuotaLimits, QuotaWithUsage } from "@/lib/api/generated/types";
import { formatBytes } from "@/lib/format";

// ── formatters (wire units → display) ────────────────────────────────────────
const fmtCount = (n: number) => n.toLocaleString();
const fmtMib = (mib: number) => formatBytes(mib * 1024 * 1024); // QuotaUsage.ramMb is MiB
const fmtGib = (gib: number) => formatBytes(gib * 1024 ** 3); // QuotaUsage.diskGb is GiB

function tone(pct: number): ProgressTone {
  if (pct >= 100) return "err";
  if (pct >= 80) return "warn";
  return "accent";
}

function QuotaBar({
  label,
  used,
  limit,
  format,
}: {
  label: string;
  used: number;
  limit: number | null;
  format: (n: number) => string;
}) {
  if (limit == null) {
    return (
      <div className="mb-3 last:mb-0">
        <div className="flex items-center justify-between text-[13px]">
          <span>{label}</span>
          <span className="tabular-nums text-ink-2">{format(used)} · Unlimited</span>
        </div>
      </div>
    );
  }
  const pct = limit > 0 ? (used / limit) * 100 : used > 0 ? 100 : 0;
  return (
    <div className="mb-3 last:mb-0">
      <div className="flex items-center justify-between text-[13px]">
        <span>{label}</span>
        <span className="tabular-nums text-ink-2">
          {format(used)} / {format(limit)}
        </span>
      </div>
      <ProgressBar pct={pct} tone={tone(pct)} className="mt-1" />
    </div>
  );
}

/** Pure presentational bars for one scope's limits + usage. */
export function QuotaBars({ quota }: { quota: QuotaWithUsage }) {
  const { limits, usage } = quota;
  return (
    <div>
      <QuotaBar label="vCPU" used={usage.vcpu} limit={limits.maxVcpu ?? null} format={fmtCount} />
      <QuotaBar label="Memory" used={usage.ramMb} limit={limits.maxRamMb ?? null} format={fmtMib} />
      <QuotaBar label="Disk" used={usage.diskGb} limit={limits.maxDiskGb ?? null} format={fmtGib} />
      <QuotaBar label="Guests" used={usage.count} limit={limits.maxCount ?? null} format={fmtCount} />
    </div>
  );
}

export function allUnlimited(l: QuotaLimits): boolean {
  return l.maxVcpu == null && l.maxRamMb == null && l.maxDiskGb == null && l.maxCount == null;
}

// ── stateful card wrapper (loading / error+retry / data per DoD) ──────────────

interface QuotaQuery {
  isPending: boolean;
  isError: boolean;
  error: unknown;
  data?: QuotaWithUsage;
  refetch: () => void;
}

function errorDetail(err: unknown): string {
  if (err instanceof ApiError) return err.detail;
  if (err instanceof Error) return err.message;
  return "Request failed";
}

/** Bars in a titled Card with the full three-state treatment. */
export function QuotaBarsCard({
  title,
  subtitle,
  query,
  action,
}: {
  title: string;
  subtitle?: string;
  query: QuotaQuery;
  action?: ReactNode;
}) {
  return (
    <Card className="p-4">
      <div className="mb-3 flex items-center justify-between gap-2">
        <h3 className="text-[14px] font-semibold">{title}</h3>
        {action}
      </div>
      {subtitle ? <p className="-mt-2 mb-3 text-[12px] text-ink-2">{subtitle}</p> : null}
      {query.isPending ? (
        <div className="space-y-3" aria-hidden>
          {[0, 1, 2, 3].map((i) => (
            <div key={i}>
              <div className="mb-1 h-3 w-full animate-pulse rounded-fluent bg-hover" />
              <div className="h-1 w-full animate-pulse rounded-fluent bg-hover" />
            </div>
          ))}
        </div>
      ) : query.isError ? (
        <div>
          <div className="flex items-start gap-2 py-2">
            <Mi name="warn" size={16} color="var(--color-err)" />
            <span className="text-[13px] leading-[1.4] text-ink-2">{errorDetail(query.error)}</span>
          </div>
          <div className="mt-2">
            <Button variant="secondaryCompact" onClick={query.refetch}>
              Retry
            </Button>
          </div>
        </div>
      ) : query.data ? (
        <>
          {allUnlimited(query.data.limits) ? (
            <p className="mb-3 text-[12px] text-ink-2">No limits set — usage is unlimited.</p>
          ) : null}
          <QuotaBars quota={query.data} />
        </>
      ) : null}
    </Card>
  );
}
