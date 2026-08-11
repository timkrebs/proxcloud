// Status dot — 8px circle colored by statusColor, optional trailing label
// (design-inventory §4.6: inline-flex, align-items center, gap 6px).
import { statusColor } from "@/lib/status";

export interface StatusDotProps {
  status: string;
  label?: string;
  className?: string;
}

export function StatusDot({ status, label, className = "" }: StatusDotProps) {
  return (
    <span className={`inline-flex items-center gap-[6px] ${className}`}>
      <span
        className="h-2 w-2 shrink-0 rounded-full"
        style={{ background: statusColor(status) }}
        aria-hidden
      />
      {label != null ? <span>{label}</span> : null}
    </span>
  );
}
