// Formatting layer — the wire format is RFC3339 timestamps and raw numbers
// (bytes, 0–100 percents); these helpers produce the display strings the
// design uses ("2 h ago", "96 GB", "12%", "€36.00").

const KIB = 1024;
const UNITS = ["B", "KiB", "MiB", "GiB", "TiB", "PiB"];

/** 8214192128 → "7.7 GiB"; 0 → "0 B". */
export function formatBytes(n: number, digits = 1): string {
  if (!Number.isFinite(n) || n <= 0) return "0 B";
  const i = Math.min(Math.floor(Math.log(n) / Math.log(KIB)), UNITS.length - 1);
  const v = n / KIB ** i;
  return `${v.toFixed(i === 0 ? 0 : digits)} ${UNITS[i]}`;
}

/** Pair used/total in the larger value's unit: "96.0 / 125.8 GiB". */
export function formatBytesPair(used: number, total: number): string {
  if (total <= 0) return `${formatBytes(used)} / ${formatBytes(total)}`;
  const i = Math.min(Math.floor(Math.log(total) / Math.log(KIB)), UNITS.length - 1);
  return `${(used / KIB ** i).toFixed(1)} / ${(total / KIB ** i).toFixed(1)} ${UNITS[i]}`;
}

/** 12.345 → "12%"; clamped to [0, 100]. */
export function formatPct(pct: number, digits = 0): string {
  if (!Number.isFinite(pct)) return "—";
  return `${Math.min(100, Math.max(0, pct)).toFixed(digits)}%`;
}

/** Bytes/second → "3.2 MB/s" (decimal units, matching the design copy). */
export function formatRate(bytesPerSec: number): string {
  if (!Number.isFinite(bytesPerSec) || bytesPerSec < 0) return "—";
  if (bytesPerSec < 1000) return `${bytesPerSec.toFixed(0)} B/s`;
  const units = ["KB/s", "MB/s", "GB/s"];
  let v = bytesPerSec / 1000;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v.toFixed(1)} ${units[i]}`;
}

/** Uptime seconds → "12d 4h" / "4h 32m" / "3m". */
export function formatUptime(sec: number): string {
  if (!Number.isFinite(sec) || sec <= 0) return "—";
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

/** RFC3339 → design-style relative time: "Just now", "5 min ago", "2 h ago",
 *  "Yesterday", "3 d ago", then absolute "Mar 3, 2026 14:22". */
export function relativeTime(iso: string, now: Date = new Date()): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "—";
  const diffSec = Math.floor((now.getTime() - t.getTime()) / 1000);
  if (diffSec < 60) return "Just now";
  if (diffSec < 3600) return `${Math.floor(diffSec / 60)} min ago`;
  if (diffSec < 86400) return `${Math.floor(diffSec / 3600)} h ago`;
  if (diffSec < 2 * 86400) return "Yesterday";
  if (diffSec < 7 * 86400) return `${Math.floor(diffSec / 86400)} d ago`;
  return formatDateTime(iso);
}

/** RFC3339 → "Mar 3, 2026 14:22". */
export function formatDateTime(iso: string): string {
  const t = new Date(iso);
  if (Number.isNaN(t.getTime())) return "—";
  const date = t.toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" });
  const time = t.toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" });
  return `${date} ${time}`;
}

/** 36 + "EUR" → "€36.00". Unknown currency codes fall back to a suffix. */
export function formatMoney(amount: number, currency: string): string {
  const symbols: Record<string, string> = { EUR: "€", USD: "$", GBP: "£", CHF: "CHF " };
  const sym = symbols[currency];
  if (sym) return `${sym}${amount.toFixed(2)}`;
  return `${amount.toFixed(2)} ${currency}`;
}
