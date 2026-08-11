// Global UI chrome state — nav collapse, right-side flyout panes, and the
// command palette. Mirrors the design's state model (design-behavior.md:
// `pane: null|'tenant'|'notif'|'delete'|'json'`, `pal`, `navW 220 ⇄ 48`).
import { create } from "zustand";

export type PaneId = "tenant" | "notif" | "delete" | "json";

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
}

export const useUiStore = create<UiState>((set) => ({
  navCollapsed: false,
  toggleNav: () => set((s) => ({ navCollapsed: !s.navCollapsed })),
  openPane: null,
  setPane: (openPane) => set({ openPane }),
  paletteOpen: false,
  setPaletteOpen: (paletteOpen) => set({ paletteOpen }),
}));
