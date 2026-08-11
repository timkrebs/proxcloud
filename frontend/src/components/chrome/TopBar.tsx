"use client";

// Portal top bar — 40px dark app bar (design-inventory §2.2).
// Left: hamburger (toggles nav 220 ⇄ 48) + brand → /dashboard.
// Center: search button (opens the command palette).
// Right: bell / gear / help / user chip, all hover #3B3A39 (bg-topbar-hover).
import Link from "next/link";
import { Mi, BrandLogo } from "@/components/ui/icons";
import { useUiStore } from "@/lib/stores/uiStore";

interface TopBarProps {
  /** Unread notification count — the bell badge renders only when > 0 (§2.2.5). */
  unread?: number;
  /** Signed-in display name; the user chip renders only when both props are given. */
  username?: string;
  /** Avatar initials for the 26px circle, e.g. "AM". */
  initials?: string;
}

export default function TopBar({ unread = 0, username, initials }: TopBarProps) {
  const toggleNav = useUiStore((s) => s.toggleNav);
  const setPaletteOpen = useUiStore((s) => s.setPaletteOpen);
  const setPane = useUiStore((s) => s.setPane);

  return (
    <header className="relative z-40 flex h-10 flex-none items-center bg-topbar text-white">
      {/* Hamburger — 44×40, toggles the left nav (§2.2.1) */}
      <button
        type="button"
        title="Toggle navigation"
        aria-label="Toggle navigation"
        onClick={toggleNav}
        className="flex h-10 w-11 flex-none items-center justify-center hover:bg-topbar-hover"
      >
        <Mi name="hamburger" size={16} color="currentColor" strokeWidth={1.4} />
      </button>

      {/* Logo 18px + wordmark 15px/600 ls .2px → /dashboard (§2.2.2) */}
      <Link
        href="/dashboard"
        className="flex h-10 flex-none items-center gap-2 px-2.5 text-white hover:bg-topbar-hover hover:text-white hover:no-underline"
      >
        <BrandLogo size={18} />
        <span className="text-[15px] font-semibold tracking-[.2px]">Proxcloud</span>
      </Link>

      {/* Center search — opens the command palette (§2.2.4) */}
      <button
        type="button"
        onClick={() => setPaletteOpen(true)}
        className="absolute top-1/2 left-1/2 flex h-7 w-[min(44vw,560px)] -translate-x-1/2 -translate-y-1/2 items-center gap-2 rounded-fluent border border-topbar-hover bg-topbar-hover px-2.5 text-[13px] text-topbar-muted hover:bg-topbar-search-hover"
      >
        <Mi name="search" size={14} color="currentColor" />
        <span className="truncate">Search resources, services, and docs (Cmd+K)</span>
      </button>

      {/* Right cluster (§2.2.5) */}
      <div className="ml-auto flex h-10 items-stretch">
        <button
          type="button"
          title="Notifications"
          aria-label="Notifications"
          onClick={() => setPane("notif")}
          className="relative flex h-10 w-11 items-center justify-center hover:bg-topbar-hover"
        >
          <Mi name="bell" size={16} color="currentColor" />
          {unread > 0 && (
            <span className="absolute top-[5px] right-[7px] flex h-3.5 min-w-3.5 items-center justify-center rounded-[7px] bg-accent px-1 text-[10px] leading-none font-semibold text-white">
              {unread}
            </span>
          )}
        </button>
        <Link
          href="/settings"
          title="Settings"
          aria-label="Settings"
          className="flex h-10 w-11 items-center justify-center text-white hover:bg-topbar-hover hover:text-white"
        >
          <Mi name="gear" size={16} color="currentColor" />
        </Link>
        <button
          type="button"
          title="Help + support"
          aria-label="Help + support"
          className="flex h-10 w-11 items-center justify-center hover:bg-topbar-hover"
        >
          <Mi name="help" size={16} color="currentColor" />
        </button>
        {username && initials ? (
          <button
            type="button"
            title="Switch tenant or project"
            onClick={() => setPane("tenant")}
            className="flex h-10 items-center gap-2 px-3 hover:bg-topbar-hover"
          >
            <span className="text-right text-[12px] leading-tight font-semibold">{username}</span>
            <span className="flex h-[26px] w-[26px] flex-none items-center justify-center rounded-full bg-accent text-[11px] font-semibold text-white">
              {initials}
            </span>
          </button>
        ) : null}
      </div>
    </header>
  );
}
