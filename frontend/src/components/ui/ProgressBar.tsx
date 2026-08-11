// Progress bar — design-inventory §4.5: 4px track #F3F2F1 radius 2, accent
// fill, width = pct%. Notification variant adds transition:width .4s.

export interface ProgressBarProps {
  /** 0–100; clamped. */
  pct: number;
  /** Animate width changes (.4s, notification progress §3.13). */
  transition?: boolean;
  className?: string;
}

export function ProgressBar({ pct, transition = false, className = "" }: ProgressBarProps) {
  const clamped = Math.min(100, Math.max(0, pct));
  return (
    <div className={`h-1 rounded-fluent bg-hover ${className}`}>
      <div
        className={`h-1 rounded-fluent bg-accent ${transition ? "transition-[width] duration-[400ms]" : ""}`}
        style={{ width: `${clamped}%` }}
      />
    </div>
  );
}
