"use client";
// Dashboard — design-inventory §3.1, real Proxmox data only.
// Platform admins get the cluster-wide view (nodes, usage, health); tenant
// users get the tenant-scoped view (directory summary, their resources, cost).
import {
  CostCard,
  GuestsCard,
  NodesCard,
  RecentResourcesCard,
  ServiceHealthCard,
  ServiceTiles,
  TenantQuotaCard,
  TenantSummaryCard,
  UsageCard,
} from "@/components/dashboard/DashboardCards";
import { EmptyState } from "@/components/ui/EmptyState";
import { useCluster, useMe } from "@/lib/api/queries";
import { useActiveTenantId } from "@/lib/stores/uiStore";

function greeting(hour: number): string {
  if (hour < 12) return "Good morning";
  if (hour < 18) return "Good afternoon";
  return "Good evening";
}

function AdminDashboard() {
  const cluster = useCluster();
  return (
    <>
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
          <CostCard />
          <GuestsCard />
          <ServiceHealthCard />
        </div>
      </div>
    </>
  );
}

function TenantDashboard({ tenantName }: { tenantName?: string }) {
  return (
    <>
      <p className="mt-1 text-[13px] text-ink-2">
        {tenantName ? (
          <>
            Directory <span className="font-semibold text-ink">{tenantName}</span> · your projects and resources
          </>
        ) : (
          "Loading your directory…"
        )}
      </p>

      <ServiceTiles />

      <div className="mt-7 grid grid-cols-[minmax(0,1fr)_340px] items-start gap-4">
        <div className="flex flex-col gap-4">
          <RecentResourcesCard />
        </div>
        <div className="flex flex-col gap-4">
          <TenantSummaryCard />
          <TenantQuotaCard />
          <CostCard />
        </div>
      </div>
    </>
  );
}

export default function DashboardPage() {
  const me = useMe();
  const isAdmin = !!me.data?.isPlatformAdmin;
  const activeTenantId = useActiveTenantId();
  const tenants = me.data?.tenants ?? [];
  // Show the ACTIVE directory's name (not tenants[0], which is alphabetical and
  // wrong once a multi-tenant user switches away from the first).
  const activeTenantName = tenants.find((t) => t.id === activeTenantId)?.name ?? tenants[0]?.name;

  return (
    <div className="max-w-[1360px] px-8 pt-6 pb-10">
      <h1 className="text-[24px] font-semibold">
        {greeting(new Date().getHours())}
        {me.data ? `, ${me.data.displayName || me.data.email}` : ""}
      </h1>

      {me.data && !isAdmin && tenants.length === 0 ? (
        <EmptyState
          variant="page"
          icon="grid"
          title="No directory assigned"
          body="Your account isn't a member of any tenant yet. Ask a platform administrator to add you to one, then reload."
        />
      ) : isAdmin ? (
        <AdminDashboard />
      ) : (
        <TenantDashboard tenantName={activeTenantName} />
      )}
    </div>
  );
}
