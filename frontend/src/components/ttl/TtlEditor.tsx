"use client";
// Structured TTL editor (ADR-0020) — the field set shared by the resource blade.
// The UI collects a duration (a preset, or a custom whole-hours value) and an
// action ("stop" = reversible power-off, "delete" = irreversible destroy). A
// delete TTL additionally requires the user to type the guest name, mirroring
// the guest-delete flyout — the backend 400s a delete TTL without a matching
// confirmName. validateTtlForm is a pure function mirroring those 400s and the
// project-max ceiling, exported for unit tests. Durations are seconds on the
// wire; the editor works in hours and converts.
import { Input } from "@/components/ui/Input";
import { Mi } from "@/components/ui/icons";
import type { Ttl, TtlRequest } from "@/lib/api/generated/types";
import { formatTtlDuration } from "@/lib/ttl";

export type TtlPreset = "24h" | "48h" | "7d" | "30d" | "custom";
export type TtlAction = "stop" | "delete";

export const HOUR_SECONDS = 3600;

/** Fixed presets, in ascending seconds. "custom" is entered as whole hours. */
export const TTL_PRESETS: { key: Exclude<TtlPreset, "custom">; label: string; seconds: number }[] = [
  { key: "24h", label: "24 hours", seconds: 24 * HOUR_SECONDS },
  { key: "48h", label: "48 hours", seconds: 48 * HOUR_SECONDS },
  { key: "7d", label: "7 days", seconds: 7 * 24 * HOUR_SECONDS },
  { key: "30d", label: "30 days", seconds: 30 * 24 * HOUR_SECONDS },
];

export interface TtlFormValues {
  preset: TtlPreset;
  customHours: string; // whole hours; used only when preset === "custom"
  action: TtlAction;
  confirmName: string; // required (typed) only when action === "delete"
}

export type TtlFieldError = "duration" | "confirmName";

/** Duration in seconds implied by the form, or null when the custom value is
 *  not a positive whole number of hours. */
export function ttlFormSeconds(v: TtlFormValues): number | null {
  if (v.preset !== "custom") {
    return TTL_PRESETS.find((p) => p.key === v.preset)?.seconds ?? null;
  }
  const hours = Number(v.customHours);
  if (!Number.isInteger(hours) || hours <= 0) return null;
  return hours * HOUR_SECONDS;
}

/** Map a raw seconds value onto a preset when it matches one exactly, else a
 *  custom whole-hours value (rounding up to a whole hour if needed). */
export function secondsToPreset(seconds: number): { preset: TtlPreset; customHours: string } {
  const hit = TTL_PRESETS.find((p) => p.seconds === seconds);
  if (hit) return { preset: hit.key, customHours: "" };
  return { preset: "custom", customHours: String(Math.max(1, Math.ceil(seconds / HOUR_SECONDS))) };
}

/** Fresh defaults: prefill the project default TTL when set, else 24h / stop. */
export function defaultTtlForm(defaultTtlSeconds?: number | null): TtlFormValues {
  const seeded = defaultTtlSeconds && defaultTtlSeconds > 0 ? secondsToPreset(defaultTtlSeconds) : null;
  return {
    preset: seeded?.preset ?? "24h",
    customHours: seeded?.customHours ?? "",
    action: "stop",
    confirmName: "",
  };
}

/** Seed the form from a stored TTL (its original chosen duration + action). */
export function ttlToForm(t: Ttl): TtlFormValues {
  const { preset, customHours } = secondsToPreset(t.originalDurationSeconds);
  return {
    preset,
    customHours,
    action: t.action === "delete" ? "delete" : "stop",
    confirmName: "",
  };
}

export interface TtlValidation {
  errors: Partial<Record<TtlFieldError, string>>;
  body?: TtlRequest;
}

/** Pure client validation, mirroring the backend 400s: ttlSeconds > 0 and
 *  ≤ the project max, and — for a delete TTL — confirmName must equal the guest
 *  name. No body is produced unless the form is fully valid, so a delete TTL is
 *  never submittable without the typed confirmation. */
export function validateTtlForm(
  v: TtlFormValues,
  opts: { maxTtlSeconds: number; guestName: string },
): TtlValidation {
  const errors: Partial<Record<TtlFieldError, string>> = {};

  const seconds = ttlFormSeconds(v);
  if (seconds === null) {
    errors.duration = "Enter a whole number of hours greater than zero.";
  } else if (seconds > opts.maxTtlSeconds) {
    errors.duration = `Exceeds the project maximum of ${formatTtlDuration(opts.maxTtlSeconds)}.`;
  }

  if (v.action === "delete" && v.confirmName !== opts.guestName) {
    errors.confirmName = "Type the guest name exactly to confirm a delete TTL.";
  }

  if (Object.keys(errors).length > 0 || seconds === null) return { errors };

  const body: TtlRequest = {
    action: v.action,
    ttlSeconds: seconds,
    ...(v.action === "delete" ? { confirmName: v.confirmName } : {}),
  };
  return { errors, body };
}

// ── Presentational editor ─────────────────────────────────────────────────────

function Field({
  label,
  htmlFor,
  hint,
  error,
  children,
}: {
  label: string;
  htmlFor?: string;
  hint?: string;
  error?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="mb-4">
      <label htmlFor={htmlFor} className="mb-[6px] block text-[13px] font-semibold">
        {label}
      </label>
      {children}
      {error ? (
        <p className="mt-1 text-[12px] text-err-text">{error}</p>
      ) : hint ? (
        <p className="mt-1 text-[12px] text-ink-3">{hint}</p>
      ) : null}
    </div>
  );
}

export function TtlEditor({
  values,
  onChange,
  errors,
  maxTtlSeconds,
  guestName,
  disabled = false,
}: {
  values: TtlFormValues;
  onChange: (patch: Partial<TtlFormValues>) => void;
  errors: Partial<Record<TtlFieldError, string>>;
  maxTtlSeconds: number;
  guestName: string;
  disabled?: boolean;
}) {
  return (
    <div>
      <Field
        label="Duration"
        error={errors.duration}
        hint={`Maximum for this project: ${formatTtlDuration(maxTtlSeconds)}.`}
      >
        <div className="flex flex-wrap gap-[6px]" role="group" aria-label="TTL duration">
          {TTL_PRESETS.map((p) => {
            const overMax = p.seconds > maxTtlSeconds;
            const selected = values.preset === p.key;
            return (
              <button
                key={p.key}
                type="button"
                aria-pressed={selected}
                disabled={disabled || overMax}
                title={overMax ? "Above the project maximum" : undefined}
                onClick={() => onChange({ preset: p.key })}
                className={`h-8 rounded-fluent border px-3 text-[12px] ${
                  disabled || overMax ? "cursor-default" : "cursor-pointer"
                } ${
                  selected
                    ? "border-accent bg-selected text-accent"
                    : overMax
                      ? "border-line-input bg-card text-ink-3"
                      : "border-line-input bg-card text-ink enabled:hover:border-accent"
                }`}
              >
                {p.label}
              </button>
            );
          })}
          <button
            type="button"
            aria-pressed={values.preset === "custom"}
            disabled={disabled}
            onClick={() => onChange({ preset: "custom" })}
            className={`h-8 rounded-fluent border px-3 text-[12px] ${
              disabled ? "cursor-default" : "cursor-pointer"
            } ${
              values.preset === "custom"
                ? "border-accent bg-selected text-accent"
                : "border-line-input bg-card text-ink enabled:hover:border-accent"
            }`}
          >
            Custom
          </button>
        </div>

        {values.preset === "custom" ? (
          <div className="mt-[10px] flex items-center gap-2">
            <Input
              id="ttl-custom-hours"
              value={values.customHours}
              onChange={(e) => onChange({ customHours: e.target.value })}
              disabled={disabled}
              invalid={!!errors.duration}
              inputMode="numeric"
              className="w-[120px]"
              aria-label="Custom TTL hours"
            />
            <span className="text-[13px] text-ink-2">hours</span>
          </div>
        ) : null}
      </Field>

      <Field
        label="Action on expiry"
        error={undefined}
        hint={
          values.action === "delete"
            ? "The guest is permanently destroyed when the TTL fires."
            : "The guest is powered off (reversible) and marked expired when the TTL fires."
        }
      >
        <div className="flex gap-[6px]" role="radiogroup" aria-label="Action on expiry">
          {(["stop", "delete"] as const).map((a) => {
            const selected = values.action === a;
            return (
              <button
                key={a}
                type="button"
                role="radio"
                aria-checked={selected}
                disabled={disabled}
                onClick={() => onChange({ action: a })}
                className={`h-8 rounded-fluent border px-3 text-[12px] ${
                  disabled ? "cursor-default" : "cursor-pointer"
                } ${
                  selected
                    ? a === "delete"
                      ? "border-err bg-err-bg text-err-text"
                      : "border-accent bg-selected text-accent"
                    : "border-line-input bg-card text-ink enabled:hover:border-accent"
                }`}
              >
                {a === "stop" ? "Stop (reversible)" : "Delete (irreversible)"}
              </button>
            );
          })}
        </div>
      </Field>

      {values.action === "delete" ? (
        <div className="mb-4">
          <div className="mb-3 flex gap-[10px] rounded-fluent border border-err bg-err-bg px-3 py-[10px] text-[13px] leading-[1.5]">
            <Mi name="warn" size={16} color="var(--color-err)" style={{ flexShrink: 0, marginTop: 2 }} />
            <span>
              When this TTL fires, <strong>{guestName}</strong> and all its disks are permanently
              destroyed. This cannot be undone.
            </span>
          </div>
          <label htmlFor="ttl-confirm" className="mb-[6px] block text-[13px]">
            Type <strong>{guestName}</strong> to confirm
          </label>
          <Input
            id="ttl-confirm"
            value={values.confirmName}
            onChange={(e) => onChange({ confirmName: e.target.value })}
            disabled={disabled}
            invalid={!!errors.confirmName}
            placeholder={guestName}
            aria-label="Confirm guest name"
            className="w-full"
          />
          {errors.confirmName ? (
            <p className="mt-1 text-[12px] text-err-text">{errors.confirmName}</p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
