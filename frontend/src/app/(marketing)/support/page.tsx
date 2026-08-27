import type { Metadata } from "next";
import Link from "next/link";

import { GITHUB_URL } from "@/components/marketing/data";
import { FeatureGrid, InfoCard, PageShell } from "@/components/marketing/PageShell";

export const metadata: Metadata = {
  title: "Support — Proxcloud",
  description:
    "Get help running Proxcloud: read the docs, browse the source and open issues on GitHub, or sign in to the portal.",
};

export default function SupportPage() {
  return (
    <PageShell
      eyebrow="Support"
      title="Help running your own cloud"
      intro="Proxcloud is self-hosted, so support starts with the docs and the source. Everything the portal does is a documented call to the same public API."
    >
      <FeatureGrid>
        <InfoCard
          title="Read the docs"
          body="Setup, the API reference, and the Terraform provider — start here to connect a cluster and provision your first resource."
        />
        <InfoCard
          title="Browse the source"
          body="Proxcloud is developed in the open. Read the code, file an issue, or open a pull request on GitHub."
        />
        <InfoCard
          title="Sign in to the portal"
          body="Already connected a cluster? Head to the portal to manage tenants, quotas, and resources."
        />
      </FeatureGrid>
      <div className="mt-10 flex flex-wrap gap-3">
        <Link
          href="/docs"
          className="flex h-[46px] items-center rounded-fluent bg-accent px-7 text-[16px] font-semibold text-white hover:bg-accent-hover hover:text-white hover:no-underline"
        >
          Read the docs
        </Link>
        <a
          href={GITHUB_URL}
          target="_blank"
          rel="noopener noreferrer"
          className="flex h-[46px] items-center rounded-fluent border border-border bg-transparent px-6 text-[16px] text-ink hover:bg-alt hover:text-ink hover:no-underline"
        >
          Open GitHub
        </a>
      </div>
    </PageShell>
  );
}
