// Shared shell for the marketing sub-pages (products / solutions / pricing /
// support / docs). On-brand hero header + a constrained content column, using
// the same tokens as the landing. Server component; pages stay thin.
import type { ReactNode } from "react";

export function PageShell({
  eyebrow,
  title,
  intro,
  children,
}: {
  eyebrow: string;
  title: string;
  intro: string;
  children: ReactNode;
}) {
  return (
    <div className="bg-page">
      <section className="relative overflow-hidden border-b border-line bg-page">
        <div className="pointer-events-none absolute inset-0 [background:radial-gradient(760px_360px_at_82%_-10%,var(--color-tint)_0%,transparent_70%)]" />
        <div className="relative mx-auto max-w-[1100px] px-[clamp(16px,4vw,40px)] pt-[clamp(48px,7vw,88px)] pb-[clamp(28px,4vw,44px)]">
          <div className="text-[13px] font-semibold tracking-[0.6px] text-accent uppercase">{eyebrow}</div>
          <h1 className="mt-3 max-w-[760px] text-[clamp(30px,4.4vw,46px)] font-semibold leading-[1.1] tracking-[-0.8px] text-balance">
            {title}
          </h1>
          <p className="mt-[18px] max-w-[640px] text-[clamp(16px,1.5vw,18px)] leading-[1.55] text-ink-2 text-pretty">
            {intro}
          </p>
        </div>
      </section>
      <section className="mx-auto max-w-[1100px] px-[clamp(16px,4vw,40px)] py-[clamp(40px,6vw,72px)]">
        {children}
      </section>
    </div>
  );
}

/** Simple three-up (or auto-fit) card grid used across the sub-pages. */
export function FeatureGrid({ children }: { children: ReactNode }) {
  return (
    <div className="grid gap-4 [grid-template-columns:repeat(auto-fit,minmax(260px,1fr))]">{children}</div>
  );
}

export function InfoCard({
  title,
  body,
}: {
  title: string;
  body: string;
}) {
  return (
    <div className="rounded-lg border border-line bg-card p-6 shadow-pc">
      <div className="text-[17px] font-semibold">{title}</div>
      <p className="mt-2 text-[14.5px] leading-[1.55] text-ink-2 text-pretty">{body}</p>
    </div>
  );
}
