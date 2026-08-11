"use client";
// All resources — design-inventory §3.6 adapted to Proxmox: real guests
// with VMID/node/CPU/RAM/uptime columns (functional spec), filter pills
// (pool + type + node), search, sortable headers, bulk start/stop, and the
// design's deliberate bulk-delete refusal.
import { Suspense, useMemo, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";

import { Card } from "@/components/ui/Card";
import { Checkbox } from "@/components/ui/Checkbox";
import { Input } from "@/components/ui/Input";
import { StatusDot } from "@/components/ui/StatusDot";
import { Mi, Svc } from "@/components/ui/icons";
import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import type { GuestSummary } from "@/lib/api/generated/types";
import { usePools, useResources } from "@/lib/api/queries";
import { useGuestAction } from "@/lib/api/mutations";
import { formatBytesPair, formatPct, formatUptime } from "@/lib/format";
import { pushToast } from "@/lib/stores/toastStore";
import { useUiStore } from "@/lib/stores/uiStore";

type SortKey = "vmid" | "name" | "type" | "node" | "status" | "cpuPct" | "mem" | "uptimeSec";

const TITLES: Record<string, string> = {
  all: "All resources",
  qemu: "Virtual machines",
  lxc: "LXC containers",
};

function CommandBarButton({
  onClick,
  children,
}: {
  onClick: () => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex h-9 cursor-pointer items-center gap-[6px] border-none bg-transparent px-[10px] text-[13px] text-ink hover:bg-hover"
    >
      {children}
    </button>
  );
}

function FilterPill({ onClick, children }: { onClick?: () => void; children: React.ReactNode }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="flex h-[26px] cursor-pointer items-center gap-1 rounded-pill border border-dashed border-line-soft bg-card px-[10px] text-[12px] text-ink hover:border-accent"
    >
      {children}
    </button>
  );
}

function ResourcesPageInner() {
  const params = useSearchParams();
  const router = useRouter();
  const setPane = useUiStore((s) => s.setPane);

  const typeParam = params.get("type");
  const type = typeParam === "qemu" || typeParam === "lxc" ? typeParam : undefined;
  const [pool, setPool] = useState<string>("");
  const [node, setNode] = useState<string>("");
  const [search, setSearch] = useState("");
  const [sort, setSort] = useState<{ key: SortKey; dir: 1 | -1 }>({ key: "vmid", dir: 1 });
  const [selected, setSelected] = useState<Record<string, boolean>>({});

  const resources = useResources({ type, pool: pool || undefined, node: node || undefined });
  const pools = usePools();
  const action = useGuestAction();

  const rows = useMemo(() => {
    const list = (resources.data ?? []).filter(
      (g) =>
        search === "" ||
        g.name.toLowerCase().includes(search.toLowerCase()) ||
        String(g.vmid).includes(search),
    );
    const val = (g: GuestSummary): string | number => {
      switch (sort.key) {
        case "mem":
          return g.memUsed;
        case "vmid":
        case "cpuPct":
        case "uptimeSec":
          return g[sort.key];
        default:
          return g[sort.key] ?? "";
      }
    };
    return [...list].sort((a, b) => {
      const av = val(a);
      const bv = val(b);
      const cmp = typeof av === "number" && typeof bv === "number" ? av - bv : String(av).localeCompare(String(bv));
      return cmp * sort.dir;
    });
  }, [resources.data, search, sort]);

  const selectedRows = rows.filter((g) => selected[g.id]);
  const selCount = selectedRows.length;

  const bulk = (act: "start" | "stop", eligible: (g: GuestSummary) => boolean) => {
    const targets = selectedRows.filter((g) => !g.template && eligible(g));
    if (targets.length === 0) {
      pushToast({ kind: "info", title: "Nothing to do", desc: `No selected guest can ${act} right now.` });
      return;
    }
    for (const g of targets) {
      action.mutate({ target: { node: g.node, type: g.type as "qemu" | "lxc", vmid: g.vmid, name: g.name }, action: act });
    }
    setSelected({});
  };

  const sortHeader = (key: SortKey, label: string, extra = "") => (
    <th
      key={key}
      onClick={() => setSort((s) => (s.key === key ? { key, dir: s.dir === 1 ? -1 : 1 } : { key, dir: 1 }))}
      className={`cursor-pointer border-b border-line bg-hover px-3 py-2 text-left font-semibold select-none ${extra}`}
      title={`Sort by ${label}`}
    >
      {label}
      {sort.key === key ? <span className="ml-1 text-ink-2">{sort.dir === 1 ? "↑" : "↓"}</span> : null}
    </th>
  );

  const title = TITLES[type ?? "all"];

  return (
    <div className="max-w-[1360px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">{title}</span>
      </nav>
      <h1 className="mb-3 text-[24px] font-semibold">{title}</h1>

      {/* command bar §3.6 */}
      <div className="mb-3 flex items-center border-b border-line">
        <CommandBarButton onClick={() => router.push("/create")}>
          <Mi name="plus" size={14} color="var(--color-accent)" />
          Create
        </CommandBarButton>
        <CommandBarButton onClick={() => resources.refetch()}>
          <Mi name="restart" size={14} />
          Refresh
        </CommandBarButton>
      </div>

      {/* filter pills + search */}
      <div className="mb-3 flex flex-wrap items-center gap-2">
        <FilterPill onClick={() => setPane("tenant")}>
          Pool == {pool === "" ? "all" : pool}
        </FilterPill>
        {(pools.data ?? []).length > 0 ? (
          <select
            value={pool}
            onChange={(e) => setPool(e.target.value)}
            className="h-[26px] rounded-pill border border-dashed border-line-soft bg-card px-2 text-[12px] outline-none hover:border-accent"
            aria-label="Filter by pool"
          >
            <option value="">All pools</option>
            {(pools.data ?? []).map((p) => (
              <option key={p.poolId} value={p.poolId}>
                {p.poolId}
              </option>
            ))}
          </select>
        ) : null}
        <FilterPill
          onClick={() => {
            if (type) router.push("/resources");
          }}
        >
          Type == {type ? TITLES[type] : "all"}
          {type ? <Mi name="close" size={11} color="var(--color-ink-2)" /> : null}
        </FilterPill>
        {node !== "" ? (
          <FilterPill onClick={() => setNode("")}>
            Node == {node}
            <Mi name="close" size={11} color="var(--color-ink-2)" />
          </FilterPill>
        ) : null}
        <div className="ml-auto w-[260px]">
          <Input
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Filter by name or VMID"
            aria-label="Search resources"
          />
        </div>
      </div>

      {/* bulk bar §3.6 */}
      {selCount > 0 ? (
        <div className="mb-[10px] flex items-center gap-1 rounded-fluent border border-nav-active bg-selected px-[10px] py-1 text-[13px]">
          <span className="mr-2 font-semibold">{selCount} selected</span>
          <button
            type="button"
            className="h-7 cursor-pointer border-none bg-transparent px-2 text-[13px] text-accent hover:bg-nav-active"
            onClick={() => bulk("start", (g) => g.status === "stopped")}
          >
            Start
          </button>
          <button
            type="button"
            className="h-7 cursor-pointer border-none bg-transparent px-2 text-[13px] text-accent hover:bg-nav-active"
            onClick={() => bulk("stop", (g) => g.status === "running")}
          >
            Stop
          </button>
          <button
            type="button"
            className="h-7 cursor-pointer border-none bg-transparent px-2 text-[13px] text-err hover:bg-nav-active"
            onClick={() =>
              pushToast({
                kind: "err",
                title: "Bulk delete",
                desc: "Destructive bulk actions require type-to-confirm per resource — open each guest to delete it.",
              })
            }
          >
            Delete
          </button>
          <button
            type="button"
            className="ml-auto h-7 cursor-pointer border-none bg-transparent px-2 text-[13px] text-ink-2 hover:bg-nav-active"
            onClick={() => setSelected({})}
          >
            Clear selection
          </button>
        </div>
      ) : null}

      <Card>
        {resources.isPending ? (
          <div className="space-y-2 p-4">
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
          </div>
        ) : resources.isError ? (
          <div className="p-4">
            <CardError err={resources.error} />
          </div>
        ) : rows.length === 0 ? (
          <p className="p-6 text-[13px] text-ink-2">
            No {title.toLowerCase()} found{search ? ` matching "${search}"` : ""}.
          </p>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-[13px]">
              <thead>
                <tr>
                  <th className="w-10 border-b border-line bg-hover px-2 py-2">
                    <Checkbox
                      size="table"
                      checked={selCount > 0 && selCount === rows.length}
                      onChange={(on) =>
                        setSelected(on ? Object.fromEntries(rows.map((g) => [g.id, true])) : {})
                      }
                      aria-label="Select all"
                    />
                  </th>
                  {sortHeader("name", "Name")}
                  {sortHeader("vmid", "VMID")}
                  {sortHeader("type", "Type")}
                  {sortHeader("node", "Node")}
                  {sortHeader("status", "Status")}
                  {sortHeader("cpuPct", "CPU")}
                  {sortHeader("mem", "RAM")}
                  {sortHeader("uptimeSec", "Uptime")}
                  <th className="border-b border-line bg-hover px-3 py-2 text-left font-semibold">Tags</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((g) => (
                  <tr key={g.id} className={`border-b border-line-row ${selected[g.id] ? "bg-selected" : ""}`}>
                    <td className="h-10 px-2">
                      <Checkbox
                        size="table"
                        checked={!!selected[g.id]}
                        onChange={(on) => setSelected((s) => ({ ...s, [g.id]: on }))}
                        aria-label={`Select ${g.name}`}
                      />
                    </td>
                    <td className="px-3">
                      <Link href={`/resources/${g.node}/${g.id}`} className="inline-flex items-center gap-2">
                        <Svc name={g.type === "qemu" ? "vm" : "lxc"} size={18} />
                        {g.name}
                      </Link>
                      {g.template ? (
                        <span className="ml-2 rounded-fluent border border-line bg-hover px-2 py-[2px] text-[11px] text-ink-2">
                          template
                        </span>
                      ) : null}
                    </td>
                    <td className="px-3 text-ink-2 tabular-nums">{g.vmid}</td>
                    <td className="px-3 text-ink-2">{g.type === "qemu" ? "Virtual machine" : "LXC container"}</td>
                    <td className="px-3 text-ink-2">{g.node}</td>
                    <td className="px-3">
                      <StatusDot status={g.status} label={g.status.charAt(0).toUpperCase() + g.status.slice(1)} />
                    </td>
                    <td className="px-3 text-ink-2 tabular-nums">
                      {g.status === "running" ? formatPct(g.cpuPct, 1) : "—"}
                    </td>
                    <td className="px-3 text-ink-2 tabular-nums">
                      {g.status === "running" ? formatBytesPair(g.memUsed, g.memMax) : "—"}
                    </td>
                    <td className="px-3 text-ink-2 tabular-nums">
                      {g.status === "running" ? formatUptime(g.uptimeSec) : "—"}
                    </td>
                    <td className="px-3">
                      {g.tags.map((t) => (
                        <span
                          key={t}
                          className="mr-[6px] rounded-fluent border border-line bg-hover px-2 py-[2px] text-[11px]"
                        >
                          {t}
                        </span>
                      ))}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}

export default function ResourcesPage() {
  return (
    <Suspense fallback={null}>
      <ResourcesPageInner />
    </Suspense>
  );
}
