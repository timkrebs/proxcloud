"use client";
// Notifications pane — design-inventory §3.13: 400px flyout titled
// "Notifications". Item cards with kind icon (spinner / ok / err), optional
// progress bar (.4s width transition), 11px timestamp; inline empty state;
// footer with a compact secondary "Dismiss all".
import { Mi, Spinner } from "@/components/ui/icons";
import { Button } from "@/components/ui/Button";
import { Flyout } from "@/components/ui/Flyout";
import { ProgressBar } from "@/components/ui/ProgressBar";

export type NotificationKind = "prog" | "ok" | "err";

export interface NotificationItem {
  id: number | string;
  title: string;
  desc: string;
  time: string;
  kind: NotificationKind;
  /** 0–100; rendered as a progress bar when present (§4.10). */
  prog?: number;
}

export interface NotificationsPaneProps {
  items: NotificationItem[];
  onDismissAll: () => void;
  onClose: () => void;
}

function NotificationIcon({ kind }: { kind: NotificationKind }) {
  // §3.13 icons: prog → spinner; ok → checkC #107C10; err → warn #D13438
  // (hexes allowed only inside icon color props).
  if (kind === "prog") return <Spinner size={16} />;
  if (kind === "ok") return <Mi name="checkC" size={16} color="#107C10" />;
  return <Mi name="warn" size={16} color="#D13438" />;
}

export function NotificationsPane({ items, onDismissAll, onClose }: NotificationsPaneProps) {
  return (
    <Flyout
      title="Notifications"
      onClose={onClose}
      footer={
        <Button variant="secondaryCompact" onClick={onDismissAll}>
          Dismiss all
        </Button>
      }
    >
      {items.length === 0 ? (
        <div className="py-10 text-center text-[13px] text-ink-2">No new notifications.</div>
      ) : (
        items.map((n) => (
          <div key={n.id} className="mb-[10px] rounded-fluent border border-line p-3">
            <div className="flex items-start gap-[10px]">
              <NotificationIcon kind={n.kind} />
              <div className="min-w-0 flex-1">
                <div className="text-[13px] font-semibold">{n.title}</div>
                <div className="mt-[3px] text-[12px] leading-[1.45] text-ink-2">{n.desc}</div>
                {n.prog !== undefined ? <ProgressBar pct={n.prog} transition className="mt-2" /> : null}
                <div className="mt-[6px] text-[11px] text-ink-3">{n.time}</div>
              </div>
            </div>
          </div>
        ))
      )}
    </Flyout>
  );
}
