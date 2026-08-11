// Toast state — design-behavior.md §4.10: toasts [{id, title, desc, kind}],
// kind ∈ 'ok' | 'info' | 'err', auto-dismiss after 4200 ms.
import { create } from "zustand";

export type ToastKind = "ok" | "info" | "err";

export interface Toast {
  id: number;
  title: string;
  desc: string;
  kind: ToastKind;
}

/** Auto-dismiss delay (design-inventory §2.7). */
export const TOAST_TTL_MS = 4200;

interface ToastState {
  toasts: Toast[];
  /** Push a toast; it removes itself after 4200 ms (§2.7). */
  pushToast: (t: Omit<Toast, "id">) => void;
  dismiss: (id: number) => void;
}

let nextId = 1;

export const useToastStore = create<ToastState>((set) => ({
  toasts: [],
  pushToast: (t) => {
    const id = nextId++;
    set((s) => ({ toasts: [...s.toasts, { ...t, id }] }));
    setTimeout(() => {
      set((s) => ({ toasts: s.toasts.filter((x) => x.id !== id) }));
    }, TOAST_TTL_MS);
  },
  dismiss: (id) => set((s) => ({ toasts: s.toasts.filter((x) => x.id !== id) })),
}));

/** Imperative helper for non-component call sites (stores, mutations). */
export function pushToast(t: Omit<Toast, "id">) {
  useToastStore.getState().pushToast(t);
}
