import type { Metadata } from "next";
import Link from "next/link";

import { serviceCards } from "@/components/marketing/data";
import { FeatureGrid, PageShell } from "@/components/marketing/PageShell";

export const metadata: Metadata = {
  title: "Products — Proxcloud",
  description:
    "The Proxcloud service catalog: virtual machines, Kubernetes, databases, networking, storage, and more — all provisioned from one governed catalog.",
};

export default function ProductsPage() {
  return (
    <PageShell
      eyebrow="Products"
      title="One catalog, every service your teams need"
      intro="Every Proxcloud service is provisioned from the same catalog, governed by the same quotas, and billed against the same showback model. Here is what ships today and what is on the roadmap."
    >
      <FeatureGrid>
        {serviceCards.map((s) => (
          <div key={s.name} className="rounded-lg border border-line bg-card p-6 shadow-pc">
            <div className="flex items-center gap-3">
              {s.icon}
              <span className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-[18px] font-semibold">
                {s.name}
                {s.soon && (
                  <span className="rounded-[10px] border border-line bg-alt px-2 py-[2px] text-[11px] font-semibold text-ink-2">
                    Coming soon
                  </span>
                )}
              </span>
            </div>
            <p className="mt-3 text-[14.5px] leading-[1.55] text-ink-2 text-pretty">{s.desc}</p>
          </div>
        ))}
      </FeatureGrid>
      <div className="mt-10 flex flex-wrap gap-3">
        <Link
          href="/signin"
          className="flex h-[46px] items-center rounded-fluent bg-accent px-7 text-[16px] font-semibold text-white hover:bg-accent-hover hover:text-white hover:no-underline"
        >
          Go to portal
        </Link>
        <Link
          href="/#services"
          className="flex h-[46px] items-center rounded-fluent border border-border bg-transparent px-6 text-[16px] text-ink hover:bg-alt hover:text-ink hover:no-underline"
        >
          Back to overview
        </Link>
      </div>
    </PageShell>
  );
}
