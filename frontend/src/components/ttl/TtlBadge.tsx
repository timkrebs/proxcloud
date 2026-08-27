"use client";
// TTL chip: a live-ticking countdown to a guest's expiry ("Expires in 2d 4h"),
// or a red "Expired" badge once the expiry has passed. The action (stop/delete)
// tints the countdown so a destructive TTL reads as such at a glance. Derived
// entirely client-side from the guest's TTL row (expiresAt + action), since the
// guest read models do not yet carry the expiry.
import { Mi } from "@/components/ui/icons";
import type { Ttl } from "@/lib/api/generated/types";
import { formatCountdown } from "@/lib/format";
import { isTtlExpired } from "@/lib/ttl";
import { useNow } from "@/lib/useCountdown";

export function TtlBadge({ ttl }: { ttl: Ttl }) {
  const now = useNow(30_000);
  const expired = isTtlExpired(ttl, now);
  const destroy = ttl.action === "delete";

  if (expired) {
    return (
      <span className="inline-flex items-center gap-1 rounded-fluent border border-err bg-err-bg px-2 py-[2px] text-[11px] text-err-text">
        <Mi name="warn" size={11} color="currentColor" />
        Expired
      </span>
    );
  }

  return (
    <span
      className={`inline-flex items-center gap-1 rounded-fluent border px-2 py-[2px] text-[11px] tabular-nums ${
        destroy ? "border-err bg-err-bg text-err-text" : "border-line bg-hover text-ink-2"
      }`}
      title={`${destroy ? "Deletes" : "Stops"} ${new Date(ttl.expiresAt).toLocaleString()}`}
    >
      <Mi name={destroy ? "trash" : "bolt"} size={11} color="currentColor" />
      Expires {formatCountdown(ttl.expiresAt, now)}
    </span>
  );
}
