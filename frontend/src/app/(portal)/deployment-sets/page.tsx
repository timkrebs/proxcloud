"use client";
// Deployment sets — the tenant's cluster sets (ADR-0029). A searchable table of
// name / service / status / member count / created, with the three required
// states: loading skeleton, empty (CTA to the catalog), and CardError. The
// feature is backend-flag-gated (DEPLOYMENT_SETS_ENABLED); a list 404 is treated
// as "disabled" and rendered as an empty state, exactly like the catalog.
import { useMemo } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { StatusPill } from "@/components/ui/StatusPill";
import { Mi, Svc } from "@/components/ui/icons";
import type { CatalogService } from "@/lib/api/generated/types";
import { isSetsDisabled, useDeploymentSets } from "@/lib/api/deploymentSets";
import { useServiceCatalog } from "@/lib/api/serviceCatalog";
import { setBaseName } from "@/lib/deploymentSetView";
import { formatDateTime } from "@/lib/format";

export default function DeploymentSetsPage() {
  const router = useRouter();
  const sets = useDeploymentSets();
  const catalog = useServiceCatalog();

  // Map serviceId → display name for the Service column (best-effort; the catalog
  // may itself be disabled, in which case we fall back to the raw service id).
  const serviceName = useMemo(() => {
    const byId = new Map<string, CatalogService>();
    for (const svc of catalog.data ?? []) byId.set(svc.id, svc);
    return (id: string) => byId.get(id)?.displayName ?? id;
  }, [catalog.data]);

  return (
    <div className="max-w-[1360px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">Deployment sets</span>
      </nav>
      <h1 className="mb-1 text-[24px] font-semibold">Deployment sets</h1>
      <p className="mb-4 max-w-[720px] text-[13px] leading-[1.5] text-ink-2">
        Clusters provisioned as one action — a control plane plus its workers, sharing a lifecycle.
        Start, stop, or delete the whole set from its detail page.
      </p>

      <div className="mb-3 flex items-center border-b border-line">
        <button
          type="button"
          onClick={() => router.push("/create")}
          className="flex h-9 cursor-pointer items-center gap-[6px] border-none bg-transparent px-[10px] text-[13px] text-ink hover:bg-hover"
        >
          <Mi name="plus" size={14} color="var(--color-accent)" />
          Create
        </button>
        <button
          type="button"
          onClick={() => sets.refetch()}
          className="flex h-9 cursor-pointer items-center gap-[6px] border-none bg-transparent px-[10px] text-[13px] text-ink hover:bg-hover"
        >
          <Mi name="restart" size={14} />
          Refresh
        </button>
      </div>

      {sets.isPending ? (
        <Card>
          <div className="space-y-2 p-4">
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
          </div>
        </Card>
      ) : sets.isError && !isSetsDisabled(sets.error) ? (
        <CardError err={sets.error} />
      ) : (sets.data ?? []).length === 0 ? (
        // A disabled (404) feature and a genuinely empty list look the same here —
        // both invite the operator to the catalog, no capability leak.
        <EmptyState
          icon="grid"
          title="No deployment sets yet"
          body="Provision a cluster service (like K3s) from the catalog to create your first deployment set."
          cta={{ label: "Browse the catalog", onClick: () => router.push("/create") }}
        />
      ) : (
        <Card>
          <div className="overflow-x-auto">
            <table className="w-full border-collapse text-[13px]">
              <thead>
                <tr>
                  {["Name", "Service", "Status", "Members", "Created"].map((h) => (
                    <th
                      key={h}
                      className="border-b border-line bg-hover px-3 py-2 text-left font-semibold"
                    >
                      {h}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {(sets.data ?? []).map((set) => (
                  <tr key={set.id} className="border-b border-line-row last:border-b-0">
                    <td className="px-3">
                      <Link
                        href={`/deployment-sets/${set.id}`}
                        className="inline-flex items-center gap-2"
                      >
                        <Svc name="k8s" size={18} />
                        {setBaseName(set)}
                      </Link>
                    </td>
                    <td className="px-3 text-ink-2">{serviceName(set.serviceId)}</td>
                    <td className="px-3">
                      <StatusPill status={set.status} />
                    </td>
                    <td className="px-3 text-ink-2 tabular-nums">{set.members.length}</td>
                    <td className="px-3 text-ink-2">{formatDateTime(set.createdAt)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </Card>
      )}
    </div>
  );
}
