"use client";
// Structured auto-shutdown editor — the single field set shared by the resource
// blade and the project flyout. The UI never touches cron: it collects a
// shutdown time, an optional auto-start time, a days-of-week set (0..6, Sun..Sat),
// a timezone, a grace window, and enabled/opt-out flags. validateScheduleForm is
// a pure function mirroring the backend 400s, exported for unit tests.
import { Input } from "@/components/ui/Input";
import { Toggle } from "@/components/ui/Toggle";
import { TimezonePicker } from "@/components/schedule/TimezonePicker";
import type { Schedule, ScheduleRequest } from "@/lib/api/generated/types";
import { DAY_LABELS, parseHhmm } from "@/lib/schedule";

export interface ScheduleFormValues {
  shutdownTime: string; // "HH:MM"
  autoStartEnabled: boolean;
  autoStartTime: string; // "HH:MM"; used only when autoStartEnabled
  daysOfWeek: number[]; // 0..6 (Sun..Sat)
  timezone: string;
  graceSeconds: string; // kept as a string for the number input
  enabled: boolean;
  optOut: boolean; // resource scope only
}

export type ScheduleFieldError =
  "shutdownTime" | "autoStartTime" | "daysOfWeek" | "timezone" | "graceSeconds";

export const DEFAULT_GRACE_SECONDS = 120;

/** Fresh form defaults (weekday nights, 22:00, 120s grace) in the given zone. */
export function defaultScheduleForm(timezone: string): ScheduleFormValues {
  return {
    shutdownTime: "22:00",
    autoStartEnabled: false,
    autoStartTime: "07:00",
    daysOfWeek: [1, 2, 3, 4, 5],
    timezone,
    graceSeconds: String(DEFAULT_GRACE_SECONDS),
    enabled: true,
    optOut: false,
  };
}

/** Seed the form from a stored schedule. */
export function scheduleToForm(s: Schedule): ScheduleFormValues {
  return {
    shutdownTime: s.shutdownTime,
    autoStartEnabled: !!s.autoStartTime,
    autoStartTime: s.autoStartTime ?? "07:00",
    daysOfWeek: [...s.daysOfWeek].sort((a, b) => a - b),
    timezone: s.timezone,
    graceSeconds: String(s.graceSeconds),
    enabled: s.enabled,
    optOut: s.optOut,
  };
}

export interface ScheduleValidation {
  errors: Partial<Record<ScheduleFieldError, string>>;
  body?: ScheduleRequest;
}

/** Pure client validation, mirroring the backend 400s. optOut is emitted on
 *  resource scope only (the backend ignores it for projects). */
export function validateScheduleForm(
  v: ScheduleFormValues,
  scope: "resource" | "project",
): ScheduleValidation {
  const errors: Partial<Record<ScheduleFieldError, string>> = {};

  if (!parseHhmm(v.shutdownTime)) {
    errors.shutdownTime = "Enter a valid time (HH:MM, 24-hour).";
  }
  if (v.autoStartEnabled && !parseHhmm(v.autoStartTime)) {
    errors.autoStartTime = "Enter a valid time (HH:MM, 24-hour).";
  }
  const days = Array.from(new Set(v.daysOfWeek.filter((d) => d >= 0 && d <= 6))).sort(
    (a, b) => a - b,
  );
  if (days.length === 0) {
    errors.daysOfWeek = "Select at least one day.";
  }
  if (v.timezone.trim() === "") {
    errors.timezone = "Choose a time zone.";
  }
  const grace = Number(v.graceSeconds);
  if (!Number.isInteger(grace) || grace < 1 || grace > 300) {
    errors.graceSeconds = "Grace must be a whole number of seconds between 1 and 300.";
  }

  if (Object.keys(errors).length > 0) return { errors };

  const body: ScheduleRequest = {
    shutdownTime: v.shutdownTime.trim(),
    autoStartTime: v.autoStartEnabled ? v.autoStartTime.trim() : undefined,
    daysOfWeek: days,
    timezone: v.timezone.trim(),
    graceSeconds: grace,
    enabled: v.enabled,
    ...(scope === "resource" ? { optOut: v.optOut } : {}),
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

export function ScheduleEditor({
  values,
  onChange,
  errors,
  scope,
  disabled = false,
}: {
  values: ScheduleFormValues;
  onChange: (patch: Partial<ScheduleFormValues>) => void;
  errors: Partial<Record<ScheduleFieldError, string>>;
  scope: "resource" | "project";
  disabled?: boolean;
}) {
  const toggleDay = (d: number) => {
    const has = values.daysOfWeek.includes(d);
    const next = has ? values.daysOfWeek.filter((x) => x !== d) : [...values.daysOfWeek, d];
    onChange({ daysOfWeek: next.sort((a, b) => a - b) });
  };

  return (
    <div>
      <Field
        label="Shutdown time"
        htmlFor="sched-shutdown"
        error={errors.shutdownTime}
        hint="Local to the selected time zone."
      >
        <Input
          id="sched-shutdown"
          type="time"
          value={values.shutdownTime}
          onChange={(e) => onChange({ shutdownTime: e.target.value })}
          disabled={disabled}
          invalid={!!errors.shutdownTime}
          className="w-[140px]"
          aria-label="Shutdown time"
        />
      </Field>

      <Field
        label="Auto-start"
        error={errors.autoStartTime}
        hint="Optionally power the guest back on later the same day."
      >
        <div className="flex items-center gap-3">
          <Toggle
            checked={values.autoStartEnabled}
            onChange={(on) => onChange({ autoStartEnabled: on })}
            disabled={disabled}
            aria-label="Enable auto-start"
          />
          <Input
            type="time"
            value={values.autoStartTime}
            onChange={(e) => onChange({ autoStartTime: e.target.value })}
            disabled={disabled || !values.autoStartEnabled}
            invalid={!!errors.autoStartTime}
            className="w-[140px]"
            aria-label="Auto-start time"
          />
        </div>
      </Field>

      <Field label="Days" error={errors.daysOfWeek}>
        <div className="flex flex-wrap gap-[6px]" role="group" aria-label="Days of week">
          {DAY_LABELS.map((label, d) => {
            const selected = values.daysOfWeek.includes(d);
            return (
              <button
                key={d}
                type="button"
                aria-pressed={selected}
                disabled={disabled}
                onClick={() => toggleDay(d)}
                className={`h-8 w-11 rounded-fluent border text-[12px] ${
                  disabled ? "cursor-default" : "cursor-pointer"
                } ${
                  selected
                    ? "border-accent bg-selected text-accent"
                    : "border-line-input bg-card text-ink enabled:hover:border-accent"
                }`}
              >
                {label}
              </button>
            );
          })}
        </div>
      </Field>

      <Field label="Time zone" error={errors.timezone}>
        <TimezonePicker
          value={values.timezone}
          onChange={(tz) => onChange({ timezone: tz })}
          disabled={disabled}
          invalid={!!errors.timezone}
        />
      </Field>

      <Field
        label="Grace period (seconds)"
        htmlFor="sched-grace"
        error={errors.graceSeconds}
        hint="Warning-to-shutdown window sent to the guest (1–300)."
      >
        <Input
          id="sched-grace"
          value={values.graceSeconds}
          onChange={(e) => onChange({ graceSeconds: e.target.value })}
          disabled={disabled}
          invalid={!!errors.graceSeconds}
          inputMode="numeric"
          className="w-[140px]"
          aria-label="Grace period seconds"
        />
      </Field>

      <div className="mb-4 flex items-center gap-3">
        <Toggle
          checked={values.enabled}
          onChange={(on) => onChange({ enabled: on })}
          disabled={disabled}
          aria-label="Schedule enabled"
        />
        <div>
          <div className="text-[13px] font-semibold">Enabled</div>
          <div className="text-[12px] text-ink-3">
            Paused schedules keep their settings but never fire.
          </div>
        </div>
      </div>

      {scope === "resource" ? (
        <div className="flex items-center gap-3">
          <Toggle
            checked={values.optOut}
            onChange={(on) => onChange({ optOut: on })}
            disabled={disabled}
            aria-label="Exempt from project schedule"
          />
          <div>
            <div className="text-[13px] font-semibold">Exempt from project schedule</div>
            <div className="text-[12px] text-ink-3">
              Ignore the inherited project auto-shutdown for this guest.
            </div>
          </div>
        </div>
      ) : null}
    </div>
  );
}
