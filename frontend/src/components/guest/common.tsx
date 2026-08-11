"use client";
// Shared pieces of the guest blade pages.
import { useParams } from "next/navigation";

import type { GuestParams } from "@/lib/api/guestQueries";

/** Typed route params for /resources/[node]/[type]/[vmid]/*. */
export function useGuestParams(): GuestParams {
  const p = useParams<{ node: string; type: string; vmid: string }>();
  return {
    node: p.node,
    type: p.type === "lxc" ? "lxc" : "qemu",
    vmid: Number(p.vmid),
  };
}

export function BladeHeading({ children, sub }: { children: React.ReactNode; sub?: string }) {
  return (
    <h2 className="mb-3 text-[16px] font-semibold">
      {children}
      {sub ? <span className="ml-2 text-[12px] font-normal text-ink-2">· {sub}</span> : null}
    </h2>
  );
}

/** List-page table wrapper: white card, gray header row (§4.3). */
export function BladeTable({ headers, children }: { headers: string[]; children: React.ReactNode }) {
  return (
    <div className="overflow-x-auto rounded-fluent border border-line bg-card">
      <table className="w-full border-collapse text-[13px]">
        <thead>
          <tr>
            {headers.map((h) => (
              <th key={h} className="border-b border-line bg-hover px-3 py-2 text-left font-semibold">
                {h}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>{children}</tbody>
      </table>
    </div>
  );
}

export const bladeCell = "h-10 px-3";
export const bladeCellMuted = "h-10 px-3 text-ink-2";
