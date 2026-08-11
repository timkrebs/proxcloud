"use client";
// Empty states — design-inventory §4.14.
// card:  40px #C8C6C4 icon sw 1, 14px/600 title, 13px body, primary CTA,
//        inside a white card with 40px padding (snapshots §3.5.7)
// page:  44px icon, 18px/600 title, 13px body max-width 440, primary CTA,
//        80px 32px padding, centered (placeholder screens §3.8)
import { Mi, type MiName } from "@/components/ui/icons";
import { Button } from "@/components/ui/Button";

export interface EmptyStateProps {
  icon: MiName;
  title: string;
  body: string;
  cta?: { label: string; onClick: () => void };
  variant?: "card" | "page";
}

export function EmptyState({ icon, title, body, cta, variant = "card" }: EmptyStateProps) {
  if (variant === "page") {
    return (
      <div className="flex flex-col items-center px-8 py-20 text-center">
        <div className="mb-[14px]">
          <Mi name={icon} size={44} color="var(--color-line-soft)" strokeWidth={1} />
        </div>
        <div className="text-[18px] font-semibold text-ink">{title}</div>
        <p className="mt-2 mb-[18px] max-w-[440px] text-[13px] leading-[1.5] text-ink-2">{body}</p>
        {cta ? (
          <Button variant="primary" onClick={cta.onClick}>
            {cta.label}
          </Button>
        ) : null}
      </div>
    );
  }
  return (
    <div className="rounded-fluent border border-line bg-card p-10 text-center">
      <div className="mb-3 flex justify-center">
        <Mi name={icon} size={40} color="var(--color-line-soft)" strokeWidth={1} />
      </div>
      <div className="text-[14px] font-semibold text-ink">{title}</div>
      <p className="mx-auto mt-[6px] mb-4 text-[13px] leading-[1.5] text-ink-2">{body}</p>
      {cta ? (
        <Button variant="primary" onClick={cta.onClick}>
          {cta.label}
        </Button>
      ) : null}
    </div>
  );
}
