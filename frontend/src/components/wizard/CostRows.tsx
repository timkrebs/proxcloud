"use client";
// Flat-rate cost estimate — real allocations × the operator's configured
// rates. Labeled as an estimate exactly like the design's cost card.
import type { Pricing } from "@/lib/api/generated/types";
import { formatMoney } from "@/lib/format";

export function CostRows({
  pricing,
  cores,
  memoryMb,
  diskGb,
}: {
  pricing: Pricing;
  cores: number;
  memoryMb: number;
  diskGb: number;
}) {
  const ramGb = memoryMb / 1024;
  const compute = cores * pricing.vcpuMonth + ramGb * pricing.ramGbMonth;
  const disk = diskGb * pricing.diskGbMonth;
  const total = compute + disk;

  return (
    <>
      <div className="my-3 h-px bg-line" />
      <div className="flex justify-between py-[3px] text-[13px]">
        <span className="text-ink-2">
          Compute ({cores} vCPU · {ramGb.toFixed(1)} GiB)
        </span>
        <span className="tabular-nums">{formatMoney(compute, pricing.currency)}</span>
      </div>
      {diskGb > 0 ? (
        <div className="flex justify-between py-[3px] text-[13px]">
          <span className="text-ink-2">Disk ({diskGb} GiB)</span>
          <span className="tabular-nums">{formatMoney(disk, pricing.currency)}</span>
        </div>
      ) : null}
      <div className="my-2 h-px bg-line" />
      <div className="flex justify-between py-[3px] text-[14px] font-semibold">
        <span>Monthly total</span>
        <span className="tabular-nums">{formatMoney(total, pricing.currency)}</span>
      </div>
      <p className="mt-1 text-[12px] text-ink-3">Estimate · flat price model</p>
    </>
  );
}
