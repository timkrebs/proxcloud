"use client";
// One-time recovery-code reveal (Phase 5). The backend returns the ten codes
// exactly once — at TOTP enable or regenerate — and never again. This panel
// shows them, offers copy + download, and gates its "Done" action behind an
// explicit "I've saved these" acknowledgement so a user cannot dismiss the only
// copy by accident. Presentational + local-only: no query client, no network.
import { useState } from "react";

import { Button } from "@/components/ui/Button";
import { Checkbox } from "@/components/ui/Checkbox";
import { Mi } from "@/components/ui/icons";

export interface RecoveryCodesProps {
  codes: string[];
  /** Called when the user acknowledges and dismisses the reveal. */
  onDone: () => void;
  doneLabel?: string;
}

/** Copy text to the clipboard, best-effort (jsdom / insecure origins lack it). */
async function copyText(text: string): Promise<boolean> {
  try {
    if (typeof navigator !== "undefined" && navigator.clipboard) {
      await navigator.clipboard.writeText(text);
      return true;
    }
  } catch {
    // fall through — surfaced as a non-fatal "couldn't copy" hint
  }
  return false;
}

/** Trigger a .txt download of the codes, best-effort. */
function downloadText(text: string, filename: string): void {
  try {
    const blob = new Blob([text], { type: "text/plain" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  } catch {
    // no-op — the codes are still visible + copyable
  }
}

export function RecoveryCodes({ codes, onDone, doneLabel = "Done" }: RecoveryCodesProps) {
  const [saved, setSaved] = useState(false);
  const [copied, setCopied] = useState(false);
  const asText = codes.join("\n");

  return (
    <div>
      <div className="mb-3 flex gap-[10px] rounded-fluent border border-ok bg-ok-bg px-3 py-[10px] text-[13px] leading-[1.5]">
        <Mi name="checkC" size={16} color="var(--color-ok)" style={{ flexShrink: 0, marginTop: 2 }} />
        <span>
          Two-step verification is on. Save these <strong>recovery codes</strong> somewhere safe —
          each works once if you lose your authenticator, and they are shown <strong>only now</strong>.
        </span>
      </div>

      <ul className="mb-3 grid grid-cols-2 gap-x-4 gap-y-1 rounded-fluent border border-line bg-canvas px-4 py-3 font-mono text-[13px] text-ink tabular-nums">
        {codes.map((c) => (
          <li key={c}>{c}</li>
        ))}
      </ul>

      <div className="mb-4 flex gap-2">
        <Button
          variant="secondaryCompact"
          onClick={async () => setCopied(await copyText(asText))}
        >
          {copied ? "Copied" : "Copy codes"}
        </Button>
        <Button
          variant="secondaryCompact"
          onClick={() => downloadText(asText, "proxcloud-recovery-codes.txt")}
        >
          Download .txt
        </Button>
      </div>

      <label className="mb-4 flex cursor-pointer items-start gap-2 text-[13px] text-ink">
        <Checkbox checked={saved} onChange={setSaved} aria-label="I've saved these recovery codes" />
        <span>I&apos;ve saved these recovery codes in a safe place.</span>
      </label>

      <Button variant="primary" disabled={!saved} onClick={onDone}>
        {doneLabel}
      </Button>
    </div>
  );
}
