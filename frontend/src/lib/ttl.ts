// TTL helpers (ADR-0020). Durations are seconds on the wire; these produce the
// display strings the blade/badges use ("7 days", "36 hours") and the derived
// expired state (a TTL whose expiry is at or before now).
import type { Ttl } from "@/lib/api/generated/types";

const HOUR = 3600;
const DAY = 86400;

/** Whole-unit human duration: exact days when divisible, else whole hours,
 *  else rounded-up hours. 604800 → "7 days"; 129600 → "36 hours". */
export function formatTtlDuration(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return "—";
  if (seconds % DAY === 0) {
    const d = seconds / DAY;
    return `${d} day${d === 1 ? "" : "s"}`;
  }
  const h = seconds % HOUR === 0 ? seconds / HOUR : Math.ceil(seconds / HOUR);
  return `${h} hour${h === 1 ? "" : "s"}`;
}

/** A TTL is "expired" (client-derived) once its expiry is at or before now.
 *  NOTE(backend): this is a display-only derivation — the Ttl model carries no
 *  authoritative expired flag, and GuestDetail/GuestSummary carry no expiredAt,
 *  so between `expiresAt` and the scheduler tick this can lead the actual state. */
export function isTtlExpired(ttl: Ttl, now: Date = new Date()): boolean {
  const t = new Date(ttl.expiresAt);
  if (Number.isNaN(t.getTime())) return false;
  return t.getTime() <= now.getTime();
}
