// Global UI chrome state — nav collapse, right-side flyout panes, the command
// palette, and (Phase 3) the active tenant + project scope. Mirrors the
// design's state model (design-behavior.md: `pane`, `pal`, `navW 220 ⇄ 48`).
import { create } from "zustand";
import { createJSONStorage, persist, type StateStorage } from "zustand/middleware";

export type PaneId = "tenant" | "notif" | "delete" | "json";

// Persistence is a client-only concern. During SSR/prerender — and in test
// environments where localStorage is unavailable — fall back to a no-op store
// so the persist middleware never touches an undefined API.
function safeLocalStorage(): StateStorage {
  try {
    const ls = typeof window !== "undefined" ? window.localStorage : undefined;
    if (ls) {
      const probe = "__proxcloud_probe__";
      ls.setItem(probe, "1");
      ls.removeItem(probe);
      return ls;
    }
  } catch {
    // access can throw in sandboxed/SSR contexts — fall through to the no-op.
  }
  return { getItem: () => null, setItem: () => {}, removeItem: () => {} };
}

interface UiState {
  /** Left nav collapsed to the 48px icon rail (expanded = 220px). */
  navCollapsed: boolean;
  toggleNav: () => void;
  /** The single open right-side flyout pane — only one at a time (§2.8). */
  openPane: PaneId | null;
  setPane: (p: PaneId | null) => void;
  /** Command palette visibility (§3.16). */
  paletteOpen: boolean;
  setPaletteOpen: (b: boolean) => void;

  // ── Tenancy (Phase 3) ──────────────────────────────────────────────────────
  /**
   * The tenant every scoped query is issued against. Persisted so a reload
   * keeps the user's directory; hydrated on app load against /api/auth/me
   * (PortalChrome). null until the first hydration — scoped queries wait.
   */
  activeTenantId: string | null;
  setActiveTenant: (id: string | null) => void;
  /**
   * The "All resources" project (resource-group) filter, shared between the
   * directory pane and the resources table. Not persisted — a per-session view
   * filter, reset whenever the active tenant changes.
   */
  projectFilter: string | null;
  setProjectFilter: (id: string | null) => void;
}

export const useUiStore = create<UiState>()(
  persist(
    (set) => ({
      navCollapsed: false,
      toggleNav: () => set((s) => ({ navCollapsed: !s.navCollapsed })),
      openPane: null,
      setPane: (openPane) => set({ openPane }),
      paletteOpen: false,
      setPaletteOpen: (paletteOpen) => set({ paletteOpen }),

      activeTenantId: null,
      // Switching tenants also drops the project filter: a project id from the
      // old tenant must never leak into the new tenant's scoped queries.
      setActiveTenant: (activeTenantId) => set({ activeTenantId, projectFilter: null }),
      projectFilter: null,
      setProjectFilter: (projectFilter) => set({ projectFilter }),
    }),
    {
      name: "proxcloud.activeTenant",
      storage: createJSONStorage(safeLocalStorage),
      // Only the active tenant is durable; panes, nav, and the filter are
      // per-session UI that should never survive a reload.
      partialize: (s) => ({ activeTenantId: s.activeTenantId }),
    },
  ),
);

/** Reactive selector for the active tenant id (null until hydrated). */
export function useActiveTenantId(): string | null {
  return useUiStore((s) => s.activeTenantId);
}
