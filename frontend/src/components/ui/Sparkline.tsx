// Sparkline — real-data version of the design's sparkEl (design-inventory
// §4.13): viewBox "0 0 100 34", preserveAspectRatio none, two gridlines at
// y 11.3/22.6 (#F3F2F1, 0.6), closed area filled with the chart fill at .7,
// accent line 1.2 with non-scaling stroke. Values are normalized into the
// 6..92% band against `max` (defaults to the series peak), exactly like the
// design clamps its random walk. Empty series renders an honest gap.

export interface SparkPoint {
  /** ISO timestamp (or any time key) — kept for real-data callers, not drawn. */
  t: string;
  v: number;
}

export interface SparklineProps {
  points: SparkPoint[];
  /** Pixel height: 44 (dashboard cost), 48 (VM overview), 90 (Metrics blade). */
  height?: number;
  /** Normalization ceiling; defaults to the max of the series. */
  max?: number;
}

export function Sparkline({ points, height = 44, max }: SparklineProps) {
  if (points.length === 0) {
    // Honest empty state: keep the layout slot, draw nothing.
    return <div style={{ height }} aria-hidden />;
  }

  const peakRaw = max ?? Math.max(...points.map((p) => p.v));
  const peak = peakRaw > 0 ? peakRaw : 1;

  const ys = points.map((p) => {
    const clamped = Math.min(Math.max(p.v, 0), peak);
    const pct = 6 + (clamped / peak) * 86; // 6..92 band
    return Number((34 - (pct / 100) * 34).toFixed(2));
  });

  // A single point draws as a flat line across the full width.
  const xs =
    points.length === 1
      ? [0, 100]
      : points.map((_, i) => Number(((i / (points.length - 1)) * 100).toFixed(2)));
  const yy = points.length === 1 ? [ys[0], ys[0]] : ys;

  const line = yy.map((y, i) => `${i === 0 ? "M" : "L"}${xs[i]} ${y}`).join(" ");
  const area = `${line} L100 34 L0 34 Z`;

  return (
    <svg
      width="100%"
      height={height}
      viewBox="0 0 100 34"
      preserveAspectRatio="none"
      className="block"
      aria-hidden
    >
      <path d="M0 11.3h100M0 22.6h100" stroke="var(--color-line-row)" strokeWidth={0.6} fill="none" />
      <path d={area} fill="var(--color-chart-fill)" opacity={0.7} />
      <path
        d={line}
        fill="none"
        stroke="var(--color-accent)"
        strokeWidth={1.2}
        vectorEffect="non-scaling-stroke"
      />
    </svg>
  );
}
