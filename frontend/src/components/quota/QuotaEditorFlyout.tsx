"use client";
// Quota editor (Phase 4) — a shared, presentational flyout for setting a scope's
// per-dimension limits. An empty field means "unlimited" (the wire clears that
// limit to nil). For a project scope, tenantLimits enables the same per-
// dimension "≤ tenant limit" guard the backend enforces (parity with its 400),
// so the user gets the rejection before the round-trip. The backend re-checks.
import { useMemo, useState } from "react";

import { Button } from "@/components/ui/Button";
import { Flyout } from "@/components/ui/Flyout";
import { Input } from "@/components/ui/Input";
import type { QuotaLimits, SetQuotaRequest } from "@/lib/api/generated/types";

interface FieldDef {
  key: keyof QuotaLimits;
  label: string;
  unit: string;
  help?: string;
}

const FIELDS: FieldDef[] = [
  { key: "maxVcpu", label: "Max vCPU", unit: "cores" },
  { key: "maxRamMb", label: "Max memory", unit: "MiB", help: "Provisioned memory, in MiB (1024 MiB = 1 GiB)." },
  { key: "maxDiskGb", label: "Max disk", unit: "GiB", help: "Provisioned disk, in GiB." },
  { key: "maxCount", label: "Max guests", unit: "guests" },
];

type Values = Record<keyof QuotaLimits, string>;

function prefill(current: QuotaLimits): Values {
  const v = (n: number | null | undefined) => (n == null ? "" : String(n));
  return {
    maxVcpu: v(current.maxVcpu),
    maxRamMb: v(current.maxRamMb),
    maxDiskGb: v(current.maxDiskGb),
    maxCount: v(current.maxCount),
  };
}

interface Validation {
  errors: Partial<Record<keyof QuotaLimits, string>>;
  body?: SetQuotaRequest;
}

/** Pure client validation, exported for unit tests. Mirrors the backend 400s. */
export function validateQuotaForm(values: Values, tenantLimits?: QuotaLimits): Validation {
  const errors: Partial<Record<keyof QuotaLimits, string>> = {};
  const body: SetQuotaRequest = {};
  for (const f of FIELDS) {
    const raw = values[f.key].trim();
    if (raw === "") {
      body[f.key] = undefined; // absent ⇒ nil ⇒ unlimited
      continue;
    }
    const n = Number(raw);
    if (!Number.isInteger(n) || n < 0) {
      errors[f.key] = "Enter a whole number ≥ 0, or leave blank for unlimited.";
      continue;
    }
    const cap = tenantLimits?.[f.key];
    if (cap != null && n > cap) {
      errors[f.key] = `Cannot exceed the tenant limit of ${cap} ${f.unit}.`;
      continue;
    }
    body[f.key] = n;
  }
  return { errors, body: Object.keys(errors).length ? undefined : body };
}

export function QuotaEditorFlyout({
  title,
  intro,
  current,
  tenantLimits,
  pending,
  serverError,
  onSubmit,
  onClose,
}: {
  title: string;
  intro: string;
  current: QuotaLimits;
  /** Present for project scope: each limit must be ≤ the tenant's. */
  tenantLimits?: QuotaLimits;
  pending: boolean;
  serverError?: string;
  onSubmit: (body: SetQuotaRequest) => void;
  onClose: () => void;
}) {
  const [values, setValues] = useState<Values>(() => prefill(current));
  const { errors, body } = useMemo(() => validateQuotaForm(values, tenantLimits), [values, tenantLimits]);
  const valid = body !== undefined;

  return (
    <Flyout
      title={title}
      onClose={onClose}
      footer={
        <div className="flex gap-2">
          <Button
            variant="primary"
            disabled={!valid || pending}
            onClick={() => body && onSubmit(body)}
          >
            {pending ? "Saving…" : "Save"}
          </Button>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
        </div>
      }
    >
      <p className="mb-4 text-[13px] leading-[1.5] text-ink-2">{intro}</p>

      {FIELDS.map((f) => (
        <div key={f.key} className="mb-4">
          <div className="mb-[6px] text-[13px]">
            {f.label} <span className="text-ink-3">({f.unit})</span>
          </div>
          <Input
            value={values[f.key]}
            onChange={(e) => setValues((v) => ({ ...v, [f.key]: e.target.value }))}
            placeholder="Unlimited"
            aria-label={f.label}
            inputMode="numeric"
            invalid={!!errors[f.key]}
            className="w-full"
          />
          {errors[f.key] ? (
            <p className="mt-1 text-[12px] text-err-text">{errors[f.key]}</p>
          ) : f.help ? (
            <p className="mt-1 text-[12px] text-ink-3">{f.help}</p>
          ) : null}
        </div>
      ))}

      <p className="text-[12px] text-ink-2">Leave a field blank to remove that limit (unlimited).</p>
      {serverError ? <p className="mt-3 text-[12px] text-err-text">{serverError}</p> : null}
    </Flyout>
  );
}
