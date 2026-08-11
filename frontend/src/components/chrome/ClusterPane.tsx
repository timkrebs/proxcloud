"use client";
// Cluster + pool pane — design-inventory §3.12 (tenant + project pane)
// adapted to Proxmox: the cluster is the isolation boundary, resource pools
// are the scope filter. 400px flyout, radio rows, footer Done + Sign out.
import { Button } from "@/components/ui/Button";
import { Flyout } from "@/components/ui/Flyout";
import { StatusDot } from "@/components/ui/StatusDot";

export interface ClusterPool {
  poolid: string;
  comment?: string;
}

export interface ClusterPaneProps {
  clusterName?: string;
  online?: boolean;
  pools: ClusterPool[];
  /** null = "All pools". */
  selectedPool: string | null;
  onSelectPool: (poolid: string | null) => void;
  onClose: () => void;
  onSignOut: () => void;
}

/** §4.7 radio: 16px circle, accent border + inner 8px accent dot when active. */
function Radio({ active }: { active: boolean }) {
  return (
    <span
      aria-hidden
      className={`flex h-4 w-4 shrink-0 items-center justify-center rounded-full border ${
        active ? "border-accent" : "border-line-input"
      }`}
    >
      {active ? <span className="h-2 w-2 rounded-full bg-accent" /> : null}
    </span>
  );
}

/** §3.12 section label: 12px/600, uppercase, letter-spacing .3px. */
function SectionLabel({ children }: { children: string }) {
  return (
    <div className="mb-[6px] text-[12px] font-semibold tracking-[.3px] text-ink-2 uppercase">
      {children}
    </div>
  );
}

export function ClusterPane({
  clusterName,
  online,
  pools,
  selectedPool,
  onSelectPool,
  onClose,
  onSignOut,
}: ClusterPaneProps) {
  return (
    <Flyout
      title="Cluster + pool filter"
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between">
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
          <Button variant="link" onClick={onSignOut}>
            Sign out
          </Button>
        </div>
      }
    >
      <p className="mb-[14px] text-[12px] leading-[1.5] text-ink-2">
        The pool scopes what you see — resources, usage, and activity.
      </p>

      <SectionLabel>CLUSTER</SectionLabel>
      {/* Single cluster — the radio row is always selected (§3.12 row anatomy). */}
      <div className="flex items-center gap-[10px] rounded-fluent bg-selected px-[10px] py-[9px]">
        <Radio active />
        <span className="min-w-0 flex-1 truncate text-[13px] font-semibold">
          {clusterName ?? "—"}
        </span>
        <StatusDot status={online ? "online" : "offline"} />
      </div>

      <div className="my-[14px] h-px bg-line" />

      <SectionLabel>POOLS</SectionLabel>
      <button
        type="button"
        onClick={() => onSelectPool(null)}
        className={`flex w-full cursor-pointer items-center gap-[10px] rounded-fluent px-[10px] py-2 text-left text-[13px] ${
          selectedPool === null ? "bg-selected" : "hover:bg-hover"
        }`}
      >
        <Radio active={selectedPool === null} />
        <span className="min-w-0 flex-1 truncate">All pools</span>
      </button>
      {pools.map((p) => {
        const active = selectedPool === p.poolid;
        return (
          <button
            key={p.poolid}
            type="button"
            onClick={() => onSelectPool(p.poolid)}
            className={`flex w-full cursor-pointer items-center gap-[10px] rounded-fluent px-[10px] py-2 text-left ${
              active ? "bg-selected" : "hover:bg-hover"
            }`}
          >
            <Radio active={active} />
            <span className="min-w-0 flex-1">
              <span className="block truncate text-[13px]">{p.poolid}</span>
              {p.comment ? (
                <span className="block truncate text-[11px] text-ink-2">{p.comment}</span>
              ) : null}
            </span>
          </button>
        );
      })}
      {pools.length === 0 ? (
        <p className="mt-1 text-[12px] leading-[1.5] text-ink-2">
          No resource pools on this cluster yet.
        </p>
      ) : null}
    </Flyout>
  );
}
