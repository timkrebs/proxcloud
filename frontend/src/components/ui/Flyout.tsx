"use client";
// Right-side flyout pane (Azure-style blade) — design-inventory §2.8.
// fixed below the 40px top bar, 400px wide (JSON pane: 440), pane shadow,
// slides in with pcslide (.18s). Escape closes (§2.9).
import { useEffect, type ReactNode } from "react";
import { Mi } from "@/components/ui/icons";

export interface FlyoutProps {
  title: string;
  onClose: () => void;
  children: ReactNode;
  /** Rendered above a border-t rule when present (§2.8 footer). */
  footer?: ReactNode;
  /** 400 (default) or 440 (JSON view pane §3.15). */
  width?: 400 | 440;
}

export function Flyout({ title, onClose, children, footer, width = 400 }: FlyoutProps) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div
      role="dialog"
      aria-label={title}
      className={`fixed top-10 right-0 bottom-0 z-50 flex flex-col bg-card shadow-pane animate-pcslide ${
        width === 440 ? "w-[440px]" : "w-[400px]"
      }`}
    >
      <div className="flex items-start justify-between px-5 pt-4 pb-3">
        <h2 className="text-[18px] font-semibold">{title}</h2>
        <button
          type="button"
          title="Close"
          onClick={onClose}
          className="cursor-pointer p-[6px] text-ink-2 hover:text-ink"
        >
          <Mi name="close" size={14} color="currentColor" strokeWidth={1.4} />
        </button>
      </div>
      <div className="flex-1 overflow-y-auto px-5 pb-5">{children}</div>
      {footer != null ? <div className="border-t border-line px-5 py-[14px]">{footer}</div> : null}
    </div>
  );
}
