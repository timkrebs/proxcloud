import type { Metadata } from "next";
import Link from "next/link";

import { FeatureGrid, InfoCard, PageShell } from "@/components/marketing/PageShell";

export const metadata: Metadata = {
  title: "Solutions — Proxcloud",
  description:
    "How teams use Proxcloud: platform teams, managed service providers, research and lab clusters, and migrations off public cloud.",
};

const solutions = [
  {
    title: "Platform teams",
    body: "Give internal teams a self-service portal on the Proxmox capacity you already run — with quotas, RBAC, and an audit trail instead of a ticket queue.",
  },
  {
    title: "Managed service providers",
    body: "Hard tenant isolation lets you hand each customer their own projects, users, and cost view on shared hardware, with no cross-tenant visibility.",
  },
  {
    title: "Research and lab clusters",
    body: "Let researchers spin up VMs, containers, and databases on demand within their allotted quota, and reclaim capacity automatically when work wraps up.",
  },
  {
    title: "Migration from public cloud",
    body: "Move workloads onto hardware you own with a familiar, Azure-style console — no per-core licensing and no metered egress to budget around.",
  },
];

export default function SolutionsPage() {
  return (
    <PageShell
      eyebrow="Solutions"
      title="Isolation you can hand to another team"
      intro="Proxcloud fits wherever more than one team shares a Proxmox cluster. Each tenant carries its own users, networks, quotas, activity log, and cost view."
    >
      <FeatureGrid>
        {solutions.map((s) => (
          <InfoCard key={s.title} title={s.title} body={s.body} />
        ))}
      </FeatureGrid>
      <div className="mt-10 flex flex-wrap gap-3">
        <Link
          href="/signin"
          className="flex h-[46px] items-center rounded-fluent bg-accent px-7 text-[16px] font-semibold text-white hover:bg-accent-hover hover:text-white hover:no-underline"
        >
          Go to portal
        </Link>
        <Link href="/pricing" className="flex h-[46px] items-center text-[16px] font-semibold text-accent">
          See how pricing works
        </Link>
      </div>
    </PageShell>
  );
}
