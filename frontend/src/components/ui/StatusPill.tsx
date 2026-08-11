// Status pill — VM header only (design-inventory §4.6): 12px text, 1px #EDEBE9
// border, white bg, radius 2, padding 3px 8px, 8px status dot + label.
import { statusColor, statusLabel } from "@/lib/status";

export interface StatusPillProps {
  status: string;
  /** Display text; defaults to the capitalized status. */
  label?: string;
}

export function StatusPill({ status, label }: StatusPillProps) {
  return (
    <span className="inline-flex items-center gap-[6px] rounded-fluent border border-line bg-card px-2 py-[3px] text-[12px] text-ink">
      <span
        className="h-2 w-2 shrink-0 rounded-full"
        style={{ background: statusColor(status) }}
        aria-hidden
      />
      {label ?? statusLabel(status)}
    </span>
  );
}
