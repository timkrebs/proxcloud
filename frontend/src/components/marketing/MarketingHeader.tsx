"use client";
// Sticky marketing header: brand, Products mega-menu (keyboard-operable), the
// nav links, a search button (opens a toast — no real site search yet), the
// Sign in / Go to portal CTAs, and a mobile hamburger menu below 900px.
import Link from "next/link";
import { useCallback, useEffect, useId, useRef, useState } from "react";

import { megaServices, mobileLinks, navLinks } from "./data";
import { MarketingLogo } from "./icons";
import { useMarketingToast } from "./MarketingRoot";

export function MarketingHeader() {
  const [mega, setMega] = useState(false);
  const [burger, setBurger] = useState(false);
  const [scrolled, setScrolled] = useState(false);
  const megaId = useId();
  const burgerId = useId();
  const megaBtnRef = useRef<HTMLButtonElement>(null);
  const megaPanelRef = useRef<HTMLDivElement>(null);
  const firstMegaLinkRef = useRef<HTMLAnchorElement>(null);
  const { push } = useMarketingToast();

  useEffect(() => {
    const onScroll = () => setScrolled(window.scrollY > 8);
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);

  const closeMega = useCallback(() => setMega(false), []);

  // Move focus into the mega-menu when it opens (keyboard affordance).
  useEffect(() => {
    if (mega) firstMegaLinkRef.current?.focus();
  }, [mega]);

  // Close the mega-menu on outside click.
  useEffect(() => {
    if (!mega) return;
    const onDown = (e: MouseEvent) => {
      const target = e.target as Node;
      if (megaPanelRef.current?.contains(target) || megaBtnRef.current?.contains(target)) return;
      setMega(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [mega]);

  const onMegaKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === "Escape") {
      e.preventDefault();
      setMega(false);
      megaBtnRef.current?.focus();
    }
  }, []);

  return (
    <header className="sticky top-0 z-40 border-b border-line bg-nav backdrop-blur-[8px]">
      <div
        className={`transition-shadow duration-200 ${scrolled ? "shadow-[0_2px_8px_rgba(0,0,0,0.12)]" : ""}`}
      >
        <div className="mx-auto flex h-16 max-w-[1320px] items-center gap-[clamp(12px,2.5vw,28px)] px-[clamp(16px,4vw,40px)]">
          <Link
            href="/#top"
            className="flex shrink-0 items-center gap-[9px] text-ink hover:text-ink hover:no-underline"
          >
            <MarketingLogo size={26} />
            <span className="text-[18px] font-semibold tracking-[-0.1px]">Proxcloud</span>
          </Link>

          {/* Desktop nav (hidden below 900px) */}
          <nav className="ml-[6px] hidden items-center gap-[2px] min-[900px]:flex" aria-label="Primary">
            <button
              ref={megaBtnRef}
              type="button"
              aria-expanded={mega}
              aria-haspopup="true"
              aria-controls={megaId}
              onClick={() => setMega((v) => !v)}
              onMouseEnter={() => setMega(true)}
              className={`flex h-10 cursor-pointer items-center gap-[5px] rounded-fluent border-none px-3 text-[15px] text-ink hover:bg-alt ${
                mega ? "bg-alt" : "bg-transparent"
              }`}
            >
              Products
              <svg
                width="11"
                height="11"
                viewBox="0 0 16 16"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.6"
                strokeLinecap="round"
                strokeLinejoin="round"
                className={`transition-transform duration-200 ${mega ? "rotate-180" : ""}`}
                aria-hidden
              >
                <path d="M3.5 6 8 10.5 12.5 6" />
              </svg>
            </button>
            {navLinks.map((l) => (
              <Link
                key={l.label}
                href={l.href}
                className="flex h-10 items-center rounded-fluent px-3 text-[15px] text-ink hover:bg-alt hover:text-ink hover:no-underline"
              >
                {l.label}
              </Link>
            ))}
          </nav>

          <div className="ml-auto flex items-center gap-[clamp(6px,1.5vw,14px)]">
            <button
              type="button"
              aria-label="Search"
              title="Search proxcloud.example"
              onClick={() => push("Site search opens the docs index in the full product.")}
              className="flex h-9 w-9 cursor-pointer items-center justify-center rounded-fluent border-none bg-transparent text-ink-2 hover:bg-alt hover:text-ink"
            >
              <svg
                width="17"
                height="17"
                viewBox="0 0 16 16"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.4"
                strokeLinecap="round"
                aria-hidden
              >
                <path d="M7 12A5 5 0 1 0 7 2a5 5 0 0 0 0 10zM10.7 10.7 14 14" />
              </svg>
            </button>
            <Link
              href="/signin"
              className="hidden h-9 items-center px-2 text-[15px] text-ink hover:text-ink hover:underline min-[900px]:inline-flex"
            >
              Sign in
            </Link>
            <Link
              href="/signin"
              className="inline-flex h-[38px] items-center whitespace-nowrap rounded-fluent bg-accent px-[clamp(14px,2vw,20px)] text-[15px] font-semibold text-white hover:bg-accent-hover hover:text-white hover:no-underline"
            >
              Go to portal
            </Link>
            <button
              type="button"
              aria-label="Menu"
              aria-expanded={burger}
              aria-controls={burgerId}
              onClick={() => setBurger((v) => !v)}
              className="flex h-9 w-9 cursor-pointer items-center justify-center rounded-fluent border-none bg-transparent text-ink hover:bg-alt min-[900px]:hidden"
            >
              <svg
                width="18"
                height="18"
                viewBox="0 0 16 16"
                fill="none"
                stroke="currentColor"
                strokeWidth="1.5"
                strokeLinecap="round"
                aria-hidden
              >
                <path d="M2 4.5h12M2 8h12M2 11.5h12" />
              </svg>
            </button>
          </div>
        </div>
      </div>

      {/* Products mega-menu */}
      {mega && (
        <div
          id={megaId}
          ref={megaPanelRef}
          onMouseLeave={closeMega}
          onKeyDown={onMegaKeyDown}
          className="border-t border-line bg-card shadow-pc-lift"
        >
          <div className="mx-auto grid max-w-[1320px] gap-x-8 gap-y-[18px] px-[clamp(16px,4vw,40px)] pt-[26px] pb-[30px] [grid-template-columns:repeat(auto-fit,minmax(260px,1fr))]">
            {megaServices.map((s, i) => (
              <Link
                key={s.name}
                href="/#services"
                ref={i === 0 ? firstMegaLinkRef : undefined}
                onClick={closeMega}
                className="flex gap-3 rounded-[6px] p-[10px] text-ink hover:bg-tint hover:text-ink hover:no-underline"
              >
                {s.icon}
                <span className="min-w-0">
                  <span className="block text-[15px] font-semibold">{s.name}</span>
                  <span className="mt-[3px] block text-[13px] leading-[1.45] text-ink-2">{s.short}</span>
                </span>
              </Link>
            ))}
          </div>
        </div>
      )}

      {/* Mobile menu */}
      {burger && (
        <div
          id={burgerId}
          className="border-t border-line bg-card px-[clamp(16px,4vw,40px)] pt-[10px] pb-[18px] min-[900px]:hidden"
        >
          {mobileLinks.map((l) => (
            <Link
              key={l.label}
              href={l.href}
              onClick={() => setBurger(false)}
              className="block border-b border-line py-3 text-[16px] text-ink hover:text-accent"
            >
              {l.label}
            </Link>
          ))}
          <Link
            href="/signin"
            onClick={() => setBurger(false)}
            className="block pt-[14px] pb-1 text-[16px] text-accent"
          >
            Sign in to the portal
          </Link>
        </div>
      )}
    </header>
  );
}
