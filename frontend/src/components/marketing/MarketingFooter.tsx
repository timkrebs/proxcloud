"use client";
// Marketing footer: link columns, brand mark, the light/dark theme toggle
// (wired to the marketing theme controller), and the legal / copyright row.
import Link from "next/link";

import { footerCols } from "./data";
import { MarketingLogo } from "./icons";
import { useMarketingTheme } from "./MarketingRoot";

const legalLinks = [
  { label: "Privacy", href: "/#top" },
  { label: "Terms", href: "/#top" },
  { label: "Licence", href: "/#top" },
];

function FooterLink({ href, label }: { href: string; label: string }) {
  const external = href.startsWith("http");
  const className = "text-[14px] text-ink-2 hover:text-accent";
  if (external) {
    return (
      <a href={href} target="_blank" rel="noopener noreferrer" className={className}>
        {label}
      </a>
    );
  }
  return (
    <Link href={href} className={className}>
      {label}
    </Link>
  );
}

export function MarketingFooter() {
  const { theme, toggle } = useMarketingTheme();
  const isDark = theme === "dark";

  return (
    <footer className="bg-page">
      <div className="mx-auto grid max-w-[1320px] gap-8 px-[clamp(16px,4vw,40px)] pt-[clamp(40px,5vw,64px)] pb-7 [grid-template-columns:repeat(auto-fit,minmax(180px,1fr))]">
        {footerCols.map((col) => (
          <div key={col.title}>
            <div className="mb-3 text-[14px] font-semibold">{col.title}</div>
            <div className="flex flex-col gap-[9px]">
              {col.links.map((l) => (
                <FooterLink key={l.label} href={l.href} label={l.label} />
              ))}
            </div>
          </div>
        ))}
      </div>

      <div className="border-t border-line">
        <div className="mx-auto flex max-w-[1320px] flex-wrap items-center gap-4 px-[clamp(16px,4vw,40px)] pt-5 pb-8">
          <div className="flex items-center gap-2">
            <MarketingLogo size={20} />
            <span className="text-[14px] font-semibold">Proxcloud</span>
          </div>
          <button
            type="button"
            onClick={toggle}
            aria-label={isDark ? "Switch to light theme" : "Switch to dark theme"}
            className="flex h-8 cursor-pointer items-center gap-2 rounded-fluent border border-border bg-transparent px-3 text-[13px] text-ink hover:bg-alt"
          >
            {isDark ? (
              <svg
                width="15"
                height="15"
                viewBox="0 0 16 16"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.4"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden
              >
                <path d="M8 11.5A3.5 3.5 0 1 0 8 4.5a3.5 3.5 0 0 0 0 7zM8 1v1.5M8 13.5V15M1 8h1.5M13.5 8H15M3.2 3.2l1 1M11.8 11.8l1 1M12.8 3.2l-1 1M4.2 11.8l-1 1" />
              </svg>
            ) : (
              <svg
                width="15"
                height="15"
                viewBox="0 0 16 16"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.4"
                strokeLinecap="round"
                strokeLinejoin="round"
                aria-hidden
              >
                <path d="M13.5 10.2A5.8 5.8 0 0 1 6 2.6a5.9 5.9 0 1 0 7.5 7.6z" />
              </svg>
            )}
            {isDark ? "Light theme" : "Dark theme"}
          </button>
          <div className="ml-auto flex flex-wrap items-center gap-[18px] text-[13px] text-ink-2">
            {legalLinks.map((l) => (
              <Link key={l.label} href={l.href} className="text-ink-2 hover:text-accent">
                {l.label}
              </Link>
            ))}
            <span>© 2026 Proxcloud</span>
          </div>
        </div>
      </div>
    </footer>
  );
}
