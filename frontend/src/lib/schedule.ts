// Schedule helpers — pure, timezone-aware next-run computation and human
// summaries for the auto-shutdown schedules (ADR-0019). The backend owns the
// cron; the UI only ever sees shutdownTime ("HH:MM") + daysOfWeek (0..6, Sun..Sat)
// + an IANA timezone, and derives a display-only "next run" from them client-side.

export const DAY_LABELS = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"] as const;

const HHMM_RE = /^([01]\d|2[0-3]):([0-5]\d)$/;

/** "22:00" → { h: 22, m: 0 }; null for anything that is not a valid 24h HH:MM. */
export function parseHhmm(s: string): { h: number; m: number } | null {
  const match = HHMM_RE.exec(s.trim());
  if (!match) return null;
  return { h: Number(match[1]), m: Number(match[2]) };
}

/** Wall-clock calendar date of `date` as observed in `timeZone`. Throws RangeError
 *  for an unknown timezone (surfaced by callers as "no next run"). */
function wallDateParts(date: Date, timeZone: string): { year: number; month: number; day: number } {
  const dtf = new Intl.DateTimeFormat("en-US", {
    timeZone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  });
  const map: Record<string, number> = {};
  for (const p of dtf.formatToParts(date)) {
    if (p.type !== "literal") map[p.type] = Number(p.value);
  }
  return { year: map.year, month: map.month, day: map.day };
}

/** Offset (ms) of `timeZone` at the given instant: tz-ahead-of-UTC is positive. */
function tzOffsetMs(date: Date, timeZone: string): number {
  const dtf = new Intl.DateTimeFormat("en-US", {
    timeZone,
    hourCycle: "h23",
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
  });
  const map: Record<string, number> = {};
  for (const p of dtf.formatToParts(date)) {
    if (p.type !== "literal") map[p.type] = Number(p.value);
  }
  const asUtc = Date.UTC(map.year, map.month - 1, map.day, map.hour, map.minute, map.second);
  return asUtc - date.getTime();
}

/** UTC instant for wall-clock HH:MM on a given calendar date in `timeZone`.
 *  Two-pass refinement handles DST boundaries where the naive offset is stale. */
function zonedWallToUtc(
  year: number,
  month: number,
  day: number,
  hour: number,
  minute: number,
  timeZone: string,
): Date {
  const guess = Date.UTC(year, month - 1, day, hour, minute);
  let utc = guess - tzOffsetMs(new Date(guess), timeZone);
  utc = guess - tzOffsetMs(new Date(utc), timeZone);
  return new Date(utc);
}

/**
 * The next UTC instant at which `shutdownTime` falls on one of `daysOfWeek`
 * (0..6, Sun..Sat, interpreted as the wall-clock weekday in `timeZone`), strictly
 * after `now`. null when the schedule can never fire (no days / bad time / bad tz).
 */
export function nextRun(
  shutdownTime: string,
  daysOfWeek: number[],
  timeZone: string,
  now: Date = new Date(),
): Date | null {
  const hm = parseHhmm(shutdownTime);
  const days = daysOfWeek.filter((d) => d >= 0 && d <= 6);
  if (!hm || days.length === 0) return null;

  let base;
  try {
    base = wallDateParts(now, timeZone);
  } catch {
    return null; // unknown timezone
  }
  // Noon anchor so adding whole days never rolls across a DST midnight gap.
  const noonUtc = Date.UTC(base.year, base.month - 1, base.day, 12);
  for (let i = 0; i < 8; i++) {
    const dayUtc = new Date(noonUtc + i * 86_400_000);
    if (!days.includes(dayUtc.getUTCDay())) continue;
    let instant: Date;
    try {
      instant = zonedWallToUtc(
        dayUtc.getUTCFullYear(),
        dayUtc.getUTCMonth() + 1,
        dayUtc.getUTCDate(),
        hm.h,
        hm.m,
        timeZone,
      );
    } catch {
      return null;
    }
    if (instant.getTime() > now.getTime()) return instant;
  }
  return null;
}

/** [1,2,3,4,5] → "Mon–Fri"; [0,6] → "Sun, Sat"; all seven → "Every day"; [] → "". */
export function formatDays(daysOfWeek: number[]): string {
  const uniq = Array.from(new Set(daysOfWeek.filter((d) => d >= 0 && d <= 6))).sort((a, b) => a - b);
  if (uniq.length === 0) return "";
  if (uniq.length === 7) return "Every day";

  const runs: [number, number][] = [];
  for (const d of uniq) {
    const last = runs[runs.length - 1];
    if (last && d === last[1] + 1) last[1] = d;
    else runs.push([d, d]);
  }
  return runs
    .map(([a, b]) =>
      a === b
        ? DAY_LABELS[a]
        : b === a + 1
          ? `${DAY_LABELS[a]}, ${DAY_LABELS[b]}`
          : `${DAY_LABELS[a]}–${DAY_LABELS[b]}`,
    )
    .join(", ");
}

/** "Auto-shutdown 22:00 · Mon–Fri" (enabled state is decorated by the caller). */
export function scheduleSummary(s: { shutdownTime: string; daysOfWeek: number[] }): string {
  const days = formatDays(s.daysOfWeek);
  return `Auto-shutdown ${s.shutdownTime}${days ? ` · ${days}` : ""}`;
}
