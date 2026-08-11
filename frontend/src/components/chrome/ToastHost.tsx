"use client";
// Toast stack — design-inventory §2.7: fixed under the top bar (top 48px,
// right 12px, z 70), column gap 8, pointer-events pass through the container.
// Each toast: 340px card with a 3px left accent by kind, pctoast slide-in.
import { Mi, type MiName } from "@/components/ui/icons";
import { useToastStore, type ToastKind } from "@/lib/stores/toastStore";

// §2.7 accent map {ok:#107C10, info:#0078D4, err:#D13438} — referenced via
// theme tokens (no raw hex outside icon props).
const ACCENT_VAR: Record<ToastKind, string> = {
  ok: "var(--color-ok)",
  info: "var(--color-accent)",
  err: "var(--color-err)",
};

// Icon hexes are allowed inside icon color props (SVG stroke).
const ICON: Record<ToastKind, { name: MiName; color: string }> = {
  ok: { name: "checkC", color: "#107C10" },
  info: { name: "info", color: "#0078D4" },
  err: { name: "warn", color: "#D13438" },
};

export function ToastHost() {
  const toasts = useToastStore((s) => s.toasts);
  return (
    <div className="pointer-events-none fixed top-12 right-3 z-[70] flex flex-col gap-2">
      {toasts.map((t) => {
        const icon = ICON[t.kind];
        return (
          <div
            key={t.id}
            role="status"
            className="pointer-events-auto flex w-[340px] gap-[10px] rounded-fluent border border-line border-l-[3px] bg-card px-[14px] py-3 shadow-toast animate-pctoast"
            style={{ borderLeftColor: ACCENT_VAR[t.kind] }}
          >
            <Mi name={icon.name} size={16} color={icon.color} />
            <div className="min-w-0">
              <div className="text-[13px] font-semibold">{t.title}</div>
              <div className="text-[12px] leading-[1.4] text-ink-2">{t.desc}</div>
            </div>
          </div>
        );
      })}
    </div>
  );
}
