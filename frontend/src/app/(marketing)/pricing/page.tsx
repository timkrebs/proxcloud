import type { Metadata } from "next";
import Link from "next/link";

import { priceCards } from "@/components/marketing/data";
import { CheckIcon } from "@/components/marketing/icons";
import { PageShell } from "@/components/marketing/PageShell";

export const metadata: Metadata = {
  title: "Pricing — Proxcloud",
  description:
    "Proxcloud is self-hosted software with no per-core licensing and no metered egress. The only cost model that matters is the internal showback you set for your teams.",
};

export default function PricingPage() {
  return (
    <PageShell
      eyebrow="Pricing"
      title="You run it, you control the cost"
      intro="Proxcloud is self-hosted software on hardware you already own. There is no per-core licensing and no metered egress — the only cost model that matters is the one you set for your own teams."
    >
      <div className="grid max-w-[900px] gap-4 [grid-template-columns:repeat(auto-fit,minmax(300px,1fr))]">
        {priceCards.map((c) => (
          <div
            key={c.title}
            className={`rounded-lg border bg-card p-[26px] shadow-pc ${
              c.featured ? "border-accent" : "border-line"
            }`}
          >
            <div
              className={`text-[13px] font-semibold tracking-[0.5px] uppercase ${
                c.featured ? "text-accent" : "text-ink-2"
              }`}
            >
              {c.kicker}
            </div>
            <div className="mt-[10px] text-[22px] font-semibold">{c.title}</div>
            <div className="mt-[10px] text-[15px] leading-[1.55] text-ink-2 text-pretty">{c.desc}</div>
            <div className="mt-4 flex flex-col gap-2">
              {c.items.map((i) => (
                <div key={i} className="flex items-start gap-[9px] text-[14.5px] leading-[1.5]">
                  <CheckIcon size={14} />
                  <span>{i}</span>
                </div>
              ))}
            </div>
          </div>
        ))}
      </div>
      <p className="mt-8 max-w-[640px] text-[15px] leading-[1.6] text-ink-2 text-pretty">
        Detailed showback configuration — flat prices per resource type, cost roll-ups by
        tenant, project, and tag, and monthly usage exports — lives inside the portal once
        your cluster is connected.
      </p>
      <div className="mt-8">
        <Link
          href="/signin"
          className="flex h-[46px] w-fit items-center rounded-fluent bg-accent px-7 text-[16px] font-semibold text-white hover:bg-accent-hover hover:text-white hover:no-underline"
        >
          Go to portal
        </Link>
      </div>
    </PageShell>
  );
}
