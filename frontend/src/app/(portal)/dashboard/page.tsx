"use client";
// Dashboard — design-inventory §3.1, real Proxmox data only.
import {
  GuestsCard,
  NodesCard,
  RecentResourcesCard,
  ServiceHealthCard,
  ServiceTiles,
  UsageCard,
} from "@/components/dashboard/DashboardCards";
import { useCluster, useMe } from "@/lib/api/queries";

function greeting(hour: number): string {
  if (hour < 12) return "Good morning";
  if (hour < 18) return "Good afternoon";
  return "Good evening";
}

export default function DashboardPage() {
  const me = useMe();
  const cluster = useCluster();

  return (
    <div className="max-w-[1360px] px-8 pt-6 pb-10">
      <h1 className="text-[24px] font-semibold">
        {greeting(new Date().getHours())}
        {me.data ? `, ${me.data.username}` : ""}
      </h1>
      <p className="mt-1 text-[13px] text-ink-2">
        {cluster.data ? (
          <>
            Cluster <span className="font-semibold text-ink">{cluster.data.name || "standalone"}</span> · PVE{" "}
            {cluster.data.pveVersion} · All pools
          </>
        ) : (
          "Connecting to Proxmox…"
        )}
      </p>

      <ServiceTiles />

      <div className="mt-7 grid grid-cols-[minmax(0,1fr)_340px] items-start gap-4">
        <div className="flex flex-col gap-4">
          <RecentResourcesCard />
          <NodesCard />
        </div>
        <div className="flex flex-col gap-4">
          <UsageCard />
          <GuestsCard />
          <ServiceHealthCard />
        </div>
      </div>
    </div>
  );
}
