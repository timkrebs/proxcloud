"use client";
// One-time credential reveal (ADR-0026/0028). The catalog provision response
// carries a server-generated superuser password that Proxcloud does NOT store,
// log, or return again — so it is surfaced here exactly once, with an explicit
// "save it now" warning, before routing to the deployment page. Dismissing the
// blade (button, ✕, or Escape) always continues to the deployment; the value is
// gone from the server either way, so there is no "stuck" state to guard.
import { useEffect, useRef, useState } from "react";

import { Button } from "@/components/ui/Button";
import { Flyout } from "@/components/ui/Flyout";
import { Mi } from "@/components/ui/icons";
import type { ProvisionServiceResponse } from "@/lib/api/generated/types";

function CopyField({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);
  const copy = () => {
    void navigator.clipboard
      ?.writeText(value)
      .then(() => {
        setCopied(true);
        window.setTimeout(() => setCopied(false), 1500);
      })
      // Clipboard blocked (e.g. insecure context): the value stays selectable.
      .catch(() => undefined);
  };
  return (
    <div className="mb-3">
      <div className="mb-1 text-[12px] font-semibold text-ink-2">{label}</div>
      <div className="flex items-stretch gap-2">
        <code className="flex-1 rounded-fluent border border-line bg-hover px-2 py-[7px] font-mono text-[13px] break-all select-all">
          {value}
        </code>
        <button
          type="button"
          onClick={copy}
          aria-label={`Copy ${label.toLowerCase()}`}
          title={`Copy ${label.toLowerCase()}`}
          className="flex w-9 flex-none cursor-pointer items-center justify-center rounded-fluent border border-line-input bg-card text-ink-2 hover:bg-hover"
        >
          {copied ? (
            <Mi name="check" size={14} color="var(--color-ok)" strokeWidth={1.6} />
          ) : (
            <Mi name="copy" size={14} color="currentColor" />
          )}
        </button>
      </div>
    </div>
  );
}

export function CredentialReveal({
  resp,
  onContinue,
}: {
  resp: ProvisionServiceResponse;
  onContinue: () => void;
}) {
  // Move focus into the dialog for keyboard users (design/accessibility basics).
  const panelRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    panelRef.current?.focus();
  }, []);

  return (
    <Flyout
      title="Save your credential"
      onClose={onContinue}
      footer={
        <Button variant="primary" onClick={onContinue}>
          I&apos;ve saved it — go to deployment
        </Button>
      }
    >
      <div ref={panelRef} tabIndex={-1} className="outline-none">
        <div className="mb-4 flex gap-[10px] rounded-fluent border border-err bg-err-bg px-3 py-[10px] text-[13px] leading-[1.5]">
          <Mi
            name="warn"
            size={16}
            color="var(--color-err)"
            style={{ flexShrink: 0, marginTop: 2 }}
          />
          <span>
            <strong>Save this now.</strong> It is shown only once and Proxcloud does not store it.
            If you lose it, retrieve or reset it from inside the guest.
          </span>
        </div>

        {resp.username ? <CopyField label="Username" value={resp.username} /> : null}
        {resp.generatedPassword ? (
          <CopyField label="Generated password" value={resp.generatedPassword} />
        ) : null}

        <p className="mt-2 text-[12px] leading-[1.5] text-ink-2">
          The credential is injected into the guest via cloud-init on first boot. Provisioning
          continues on the deployment page.
        </p>
      </div>
    </Flyout>
  );
}
