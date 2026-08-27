// Landing-page sections — server components composing the design's content
// arrays (data.tsx) with the token utility classes. Interactive leaves (Reveal
// scroll-in, the API CodeBlock) are client components rendered as children.
import Link from "next/link";
import type { ReactElement } from "react";

import { CodeBlock } from "./CodeBlock";
import { apiPoints, featureRows, priceCards, proofs, serviceCards, steps } from "./data";
import { ArrowIcon, CheckIcon, LineIcon } from "./icons";
import { HeroMock } from "./mocks";
import { Reveal } from "./Reveal";

const SHELL = "mx-auto max-w-[1320px]";

export function Hero(): ReactElement {
  return (
    <section id="top" className="relative overflow-hidden bg-page">
      <div className="pointer-events-none absolute inset-0 [background:radial-gradient(900px_420px_at_78%_8%,var(--color-tint)_0%,transparent_70%)]" />
      <div
        className={`relative ${SHELL} grid items-center gap-[clamp(32px,5vw,64px)] px-[clamp(16px,4vw,40px)] pt-[clamp(48px,7vw,96px)] pb-[clamp(40px,6vw,80px)] [grid-template-columns:repeat(auto-fit,minmax(330px,1fr))]`}
      >
        <div>
          <div className="mb-[22px] inline-flex items-center gap-2 rounded-[14px] border border-tint2 bg-tint px-3 py-[5px] text-[13px] font-semibold text-accent">
            Self-service cloud for Proxmox VE
          </div>
          <h1 className="text-[clamp(34px,5.2vw,54px)] font-semibold leading-[1.08] tracking-[-1px] text-balance">
            Your own cloud, on your own hardware.
          </h1>
          <p className="mt-5 max-w-[560px] text-[clamp(16px,1.5vw,19px)] leading-[1.55] text-ink-2 text-pretty">
            Self-service VMs, Kubernetes, databases and networking on Proxmox — with multi-tenancy,
            quotas, and an Azure-familiar portal.
          </p>
          <div className="mt-[30px] flex flex-wrap gap-3">
            <Link
              href="/signin"
              className="flex h-[46px] items-center rounded-fluent bg-accent px-7 text-[16px] font-semibold text-white hover:bg-accent-hover hover:text-white hover:no-underline"
            >
              Go to portal
            </Link>
            <Link
              href="/docs"
              className="flex h-[46px] items-center rounded-fluent border border-border bg-transparent px-6 text-[16px] text-ink hover:bg-alt hover:text-ink hover:no-underline"
            >
              Read the docs
            </Link>
          </div>
          <div className="mt-[22px] flex items-center gap-2 text-[13px] text-ink-2">
            <svg
              width="15"
              height="15"
              viewBox="0 0 16 16"
              fill="none"
              stroke="#107C10"
              strokeWidth="1.5"
              strokeLinecap="round"
              strokeLinejoin="round"
              aria-hidden
            >
              <path d="M8 14.5A6.5 6.5 0 1 0 8 1.5a6.5 6.5 0 0 0 0 13zM5 8.2l2.2 2.2L11.2 6" />
            </svg>
            Self-hosted. Your cluster, your data, no per-core licensing.
          </div>
        </div>
        <Reveal>
          <HeroMock />
        </Reveal>
      </div>
    </section>
  );
}

export function ProofStrip(): ReactElement {
  return (
    <section className="border-y border-line bg-canvas">
      <div
        className={`${SHELL} grid gap-x-8 gap-y-6 px-[clamp(16px,4vw,40px)] py-[clamp(28px,4vw,40px)] [grid-template-columns:repeat(auto-fit,minmax(200px,1fr))]`}
      >
        {proofs.map((p) => (
          <div key={p.title} className="flex items-start gap-3">
            {p.icon}
            <div>
              <div className="text-[16px] font-semibold">{p.title}</div>
              <div className="mt-1 text-[13.5px] leading-[1.5] text-ink-2 text-pretty">
                {p.desc}
              </div>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}

export function ServiceGrid(): ReactElement {
  return (
    <section id="services" className="bg-page">
      <div className={`${SHELL} px-[clamp(16px,4vw,40px)] py-[clamp(56px,8vw,104px)]`}>
        <Reveal className="max-w-[640px]">
          <h2 className="text-[clamp(26px,3.2vw,34px)] font-semibold tracking-[-0.5px] text-balance">
            Explore Proxcloud services
          </h2>
          <p className="mt-[14px] text-[17px] leading-[1.55] text-ink-2 text-pretty">
            Every service is provisioned from the same catalog, governed by the same quotas, and
            billed against the same showback model.
          </p>
        </Reveal>
        <div className="mt-9 grid gap-4 [grid-template-columns:repeat(auto-fill,minmax(280px,1fr))]">
          {serviceCards.map((s) => (
            <Link
              key={s.name}
              href={s.href}
              className="flex min-h-[196px] flex-col gap-3 rounded-lg border border-line bg-card p-6 text-ink shadow-pc transition duration-200 hover:-translate-y-[3px] hover:border-accent hover:text-ink hover:no-underline hover:shadow-pc-lift"
            >
              {s.icon}
              <span className="flex flex-wrap items-baseline gap-x-2 gap-y-1 text-[18px] leading-[1.3] font-semibold">
                <span>{s.name}</span>
                {s.soon && (
                  <span className="rounded-[10px] border border-line bg-alt px-2 py-[2px] text-[11px] font-semibold text-ink-2">
                    Coming soon
                  </span>
                )}
              </span>
              <span className="flex-1 text-[14.5px] leading-[1.5] text-ink-2 text-pretty">
                {s.desc}
              </span>
              <span className="flex flex-wrap items-center gap-[6px] text-[14px] font-semibold text-accent">
                <span>{s.cta}</span>
                <ArrowIcon />
              </span>
            </Link>
          ))}
        </div>
      </div>
    </section>
  );
}

export function FeatureRows(): ReactElement {
  return (
    <section id="features" className="border-t border-line bg-canvas">
      <div
        className={`${SHELL} flex flex-col gap-[clamp(48px,7vw,88px)] px-[clamp(16px,4vw,40px)] py-[clamp(48px,7vw,88px)]`}
      >
        {featureRows.map((f) => (
          <Reveal
            key={f.title}
            className="grid items-center gap-[clamp(28px,4vw,56px)] [grid-template-columns:repeat(auto-fit,minmax(320px,1fr))]"
          >
            <div className={f.reversed ? "min-[820px]:order-2" : ""}>
              <div className="text-[13px] font-semibold tracking-[0.6px] text-accent uppercase">
                {f.kicker}
              </div>
              <h3 className="mt-3 text-[clamp(23px,2.6vw,29px)] font-semibold tracking-[-0.4px] text-balance">
                {f.title}
              </h3>
              <p className="mt-[14px] text-[16px] leading-[1.6] text-ink-2 text-pretty">{f.body}</p>
              <div className="mt-5 flex flex-col gap-[10px]">
                {f.bullets.map((b) => (
                  <div key={b} className="flex items-start gap-[10px] text-[15px] leading-[1.5]">
                    <CheckIcon />
                    <span>{b}</span>
                  </div>
                ))}
              </div>
              <Link
                href={f.href}
                className="mt-[22px] inline-flex items-center gap-[6px] text-[15px] font-semibold text-accent"
              >
                {f.link}
                <ArrowIcon />
              </Link>
            </div>
            <div className={`min-w-0 ${f.reversed ? "min-[820px]:order-1" : ""}`}>{f.visual}</div>
          </Reveal>
        ))}
      </div>
    </section>
  );
}

export function HowItWorks(): ReactElement {
  return (
    <section id="how" className="border-t border-line bg-page">
      <div className={`${SHELL} px-[clamp(16px,4vw,40px)] py-[clamp(56px,8vw,96px)]`}>
        <Reveal className="max-w-[600px]">
          <h2 className="text-[clamp(26px,3.2vw,34px)] font-semibold tracking-[-0.5px]">
            How it works
          </h2>
          <p className="mt-[14px] text-[17px] leading-[1.55] text-ink-2 text-pretty">
            Three steps from an existing Proxmox cluster to teams provisioning their own resources.
          </p>
        </Reveal>
        <div className="mt-10 grid gap-7 [grid-template-columns:repeat(auto-fit,minmax(260px,1fr))]">
          {steps.map((s) => (
            <div key={s.n} className="flex flex-col gap-3">
              <div className="flex items-center gap-3">
                <span className="flex h-[34px] w-[34px] shrink-0 items-center justify-center rounded-full bg-tint text-[15px] font-semibold text-accent">
                  {s.n}
                </span>
                {s.icon}
              </div>
              <div className="text-[18px] font-semibold">{s.title}</div>
              <div className="text-[15px] leading-[1.55] text-ink-2 text-pretty">{s.desc}</div>
            </div>
          ))}
        </div>
      </div>
    </section>
  );
}

export function ApiSection(): ReactElement {
  // Intentionally always-dark band (both themes) → fixed design hex, not tokens.
  return (
    <section id="api" className="bg-[#1B1A19] text-[#F3F2F1]">
      <div
        className={`${SHELL} grid items-center gap-[clamp(32px,5vw,60px)] px-[clamp(16px,4vw,40px)] py-[clamp(56px,8vw,96px)] [grid-template-columns:repeat(auto-fit,minmax(320px,1fr))]`}
      >
        <div>
          <div className="text-[13px] font-semibold tracking-[0.6px] text-[#479EF5] uppercase">
            Built for automation
          </div>
          <h2 className="mt-3 text-[clamp(26px,3.2vw,34px)] font-semibold tracking-[-0.5px] text-balance">
            Automate everything.
          </h2>
          <p className="mt-[14px] max-w-[520px] text-[16px] leading-[1.6] text-[#C8C6C4] text-pretty">
            Every action in the portal is a call to the same public API. Drive Proxcloud from the
            CLI, from Terraform, or straight from your pipeline — the portal has no private
            endpoints.
          </p>
          <div className="mt-[22px] flex flex-col gap-[10px]">
            {apiPoints.map((p) => (
              <div
                key={p}
                className="flex items-start gap-[10px] text-[15px] leading-[1.5] text-[#F3F2F1]"
              >
                <LineIcon
                  d="M3 8.5l3.2 3L13 4.5"
                  size={15}
                  strokeWidth={1.6}
                  className="mt-1 text-[#479EF5]"
                />
                <span>{p}</span>
              </div>
            ))}
          </div>
          <Link
            href="/docs"
            className="mt-[22px] inline-flex items-center gap-[6px] text-[15px] font-semibold text-[#479EF5] hover:text-[#62ABF5]"
          >
            Read the API reference
            <ArrowIcon />
          </Link>
        </div>
        <CodeBlock />
      </div>
    </section>
  );
}

export function PricingTeaser(): ReactElement {
  return (
    <section id="pricing" className="border-b border-line bg-canvas">
      <div className={`${SHELL} px-[clamp(16px,4vw,40px)] py-[clamp(56px,8vw,96px)]`}>
        <Reveal className="max-w-[620px]">
          <h2 className="text-[clamp(26px,3.2vw,34px)] font-semibold tracking-[-0.5px] text-balance">
            You run it, you control the cost
          </h2>
          <p className="mt-[14px] text-[17px] leading-[1.55] text-ink-2 text-pretty">
            Proxcloud is self-hosted software on hardware you already own. There is no per-core
            licensing and no metered egress — the only cost model that matters is the one you set
            for your own teams.
          </p>
        </Reveal>
        <div className="mt-9 grid max-w-[900px] gap-4 [grid-template-columns:repeat(auto-fit,minmax(300px,1fr))]">
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
              <div className="mt-[10px] text-[15px] leading-[1.55] text-ink-2 text-pretty">
                {c.desc}
              </div>
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
        <div className="mt-5 text-[14px] text-ink-2">
          Full breakdown on the{" "}
          <Link href="/pricing" className="text-accent">
            pricing page
          </Link>
          .
        </div>
      </div>
    </section>
  );
}

export function CtaBand(): ReactElement {
  return (
    <section className="border-b border-line bg-tint">
      <div
        className={`${SHELL} flex flex-wrap items-center justify-between gap-7 px-[clamp(16px,4vw,40px)] py-[clamp(48px,6vw,76px)]`}
      >
        <div>
          <h2 className="text-[clamp(24px,3vw,32px)] font-semibold tracking-[-0.5px] text-balance">
            Ready to run your own cloud?
          </h2>
          <p className="mt-[10px] max-w-[560px] text-[16px] leading-[1.55] text-ink-2 text-pretty">
            Point Proxcloud at a Proxmox cluster and hand your teams a portal they already know how
            to use.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Link
            href="/signin"
            className="flex h-[46px] items-center rounded-fluent bg-accent px-7 text-[16px] font-semibold text-white hover:bg-accent-hover hover:text-white hover:no-underline"
          >
            Go to portal
          </Link>
          <Link href="/docs" className="text-[16px] font-semibold text-accent">
            Read the docs
          </Link>
        </div>
      </div>
    </section>
  );
}
