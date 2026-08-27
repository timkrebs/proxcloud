import type { Metadata } from "next";
import Link from "next/link";

import { apiPoints, GITHUB_URL } from "@/components/marketing/data";
import { CheckIcon } from "@/components/marketing/icons";
import { FeatureGrid, InfoCard, PageShell } from "@/components/marketing/PageShell";

export const metadata: Metadata = {
  title: "Docs — Proxcloud",
  description:
    "Proxcloud documentation: connect a Proxmox cluster, invite tenants, and automate everything through the pcctl CLI, the Terraform provider, or the REST API.",
};

export default function DocsPage() {
  return (
    <PageShell
      eyebrow="Documentation"
      title="Connect a cluster, then automate everything"
      intro="The portal is one client of a public API — your pipelines are another. The full reference is being published alongside the open-source release; here is the shape of what it covers."
    >
      <FeatureGrid>
        <InfoCard
          title="1. Connect your Proxmox"
          body="Add cluster nodes and storage, import templates, and set the capacity Proxcloud is allowed to use."
        />
        <InfoCard
          title="2. Invite your tenants"
          body="Create a tenant per team or customer, set its quotas, and assign Owner, Contributor, or Reader roles."
        />
        <InfoCard
          title="3. Self-serve resources"
          body="Users open the portal, pick a service, and provision inside their own project — while you watch capacity."
        />
      </FeatureGrid>

      <h2 className="mt-12 text-[22px] font-semibold tracking-[-0.3px]">Automation surface</h2>
      <div className="mt-4 flex flex-col gap-[10px]">
        {apiPoints.map((p) => (
          <div key={p} className="flex items-start gap-[10px] text-[15px] leading-[1.5]">
            <CheckIcon />
            <span>{p}</span>
          </div>
        ))}
      </div>

      <div className="mt-10 flex flex-wrap gap-3">
        <a
          href={GITHUB_URL}
          target="_blank"
          rel="noopener noreferrer"
          className="flex h-[46px] items-center rounded-fluent bg-accent px-7 text-[16px] font-semibold text-white hover:bg-accent-hover hover:text-white hover:no-underline"
        >
          View on GitHub
        </a>
        <Link
          href="/#api"
          className="flex h-[46px] items-center rounded-fluent border border-border bg-transparent px-6 text-[16px] text-ink hover:bg-alt hover:text-ink hover:no-underline"
        >
          See code examples
        </Link>
      </div>
    </PageShell>
  );
}
