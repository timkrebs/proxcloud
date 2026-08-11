"use client";

// Command palette (design-inventory.md §3.16, design-behavior.md §"Command
// palette"). Opens from uiStore.paletteOpen; Cmd/Ctrl+K toggles globally
// (resetting the query, behavior doc action table `openPalette`), Escape
// closes. Rows = 3 static quick actions filtered by label substring, plus up
// to 5 resources whose name contains the (non-empty) query.
import { useEffect, useState, type ReactNode } from "react";
import { useRouter } from "next/navigation";
import { Mi, Svc } from "@/components/ui/icons";
import { useUiStore } from "@/lib/stores/uiStore";

export interface PaletteResource {
  id: string;
  name: string;
  type: string;
  node: string;
  kind: "vm" | "lxc";
}

interface QuickAction {
  icon: ReactNode;
  label: string;
  hint: string;
  href: string;
}

const QUICK_ACTIONS: QuickAction[] = [
  {
    icon: <Mi name="bolt" size={16} color="var(--color-accent)" />,
    label: "Create a virtual machine",
    hint: "Quick create",
    href: "/create/vm",
  },
  {
    icon: <Mi name="bolt" size={16} color="var(--color-accent)" />,
    label: "Create an LXC container",
    hint: "Quick create",
    href: "/create/lxc",
  },
  {
    icon: <Mi name="grid" size={16} color="var(--color-ink-2)" />,
    label: "All resources",
    hint: "Browse",
    href: "/resources",
  },
];

export default function CommandPalette({
  resources = [],
}: {
  /** Search corpus — wired to real data later; empty renders no resource rows. */
  resources?: PaletteResource[];
}) {
  const paletteOpen = useUiStore((s) => s.paletteOpen);
  const setPaletteOpen = useUiStore((s) => s.setPaletteOpen);
  const router = useRouter();
  const [query, setQuery] = useState("");

  // Global shortcuts: Cmd/Ctrl+K toggles (and resets the query), Escape
  // closes. Reads open state via getState() so the listener stays stable.
  useEffect(() => {
    function onKeyDown(e: KeyboardEvent) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setQuery("");
        setPaletteOpen(!useUiStore.getState().paletteOpen);
      } else if (e.key === "Escape" && useUiStore.getState().paletteOpen) {
        setPaletteOpen(false);
      }
    }
    window.addEventListener("keydown", onKeyDown);
    return () => window.removeEventListener("keydown", onKeyDown);
  }, [setPaletteOpen]);

  // Opening from anywhere (e.g. the top-bar search) starts with a fresh query.
  useEffect(() => {
    if (paletteOpen) setQuery("");
  }, [paletteOpen]);

  if (!paletteOpen) return null;

  const q = query.trim().toLowerCase();
  const quick = QUICK_ACTIONS.filter((a) => a.label.toLowerCase().includes(q));
  const matches = q
    ? resources.filter((r) => r.name.toLowerCase().includes(q)).slice(0, 5)
    : [];

  function go(href: string) {
    setPaletteOpen(false);
    router.push(href);
  }

  return (
    <div
      className="fixed inset-0 z-[60] flex items-start justify-center bg-black/25 pt-[110px]"
      onClick={() => setPaletteOpen(false)}
    >
      <div
        role="dialog"
        aria-label="Command palette"
        className="w-[600px] max-w-[90vw] overflow-hidden rounded-fluent bg-card shadow-palette"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="flex items-center gap-[10px] border-b border-line px-[14px]">
          <Mi name="search" size={15} color="var(--color-ink-2)" />
          <input
            autoFocus
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder='Search resources, or type "create vm"'
            aria-label="Search resources"
            className="h-11 flex-1 border-none bg-transparent text-[14px] text-ink outline-none"
          />
          <span className="rounded-fluent border border-line px-[6px] py-[2px] text-[11px] text-ink-3">
            Esc
          </span>
        </div>
        <div className="max-h-[340px] overflow-y-auto py-[6px]">
          {quick.map((a) => (
            <button
              key={a.label}
              type="button"
              onClick={() => go(a.href)}
              className="flex w-full cursor-pointer items-center gap-[10px] border-none bg-transparent px-[14px] py-[9px] text-left hover:bg-hover"
            >
              {a.icon}
              <span className="flex-1 text-[13px] text-ink">{a.label}</span>
              <span className="text-[11px] text-ink-3">{a.hint}</span>
            </button>
          ))}
          {matches.map((r) => (
            <button
              key={r.id}
              type="button"
              onClick={() => go(`/resources/${r.node}/${r.id}`)}
              className="flex w-full cursor-pointer items-center gap-[10px] border-none bg-transparent px-[14px] py-[9px] text-left hover:bg-hover"
            >
              <Svc name={r.kind} size={16} />
              <span className="flex-1 text-[13px] text-ink">{r.name}</span>
              <span className="text-[11px] text-ink-3">
                {r.type} · {r.node}
              </span>
            </button>
          ))}
        </div>
      </div>
    </div>
  );
}
