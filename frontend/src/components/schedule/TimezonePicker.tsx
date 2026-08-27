"use client";
// IANA timezone picker — a ui/Select populated from Intl.supportedValuesOf.
// Older runtimes that lack supportedValuesOf fall back to a small common list
// (plus whatever value is already selected, so a stored zone is never dropped).
import { Select } from "@/components/ui/Select";

/** A representative fallback for runtimes without Intl.supportedValuesOf. */
const FALLBACK_ZONES = [
  "UTC",
  "Europe/London",
  "Europe/Berlin",
  "Europe/Paris",
  "Europe/Madrid",
  "Europe/Moscow",
  "America/New_York",
  "America/Chicago",
  "America/Denver",
  "America/Los_Angeles",
  "America/Sao_Paulo",
  "Asia/Kolkata",
  "Asia/Shanghai",
  "Asia/Tokyo",
  "Australia/Sydney",
];

interface SupportsValues {
  supportedValuesOf?: (key: "timeZone") => string[];
}

/** All IANA zones the runtime knows, or the fallback list. "UTC" is always
 *  offered first (supportedValuesOf lists it as "Etc/UTC"). Exported for tests. */
export function timezoneOptions(): string[] {
  const intl = Intl as unknown as SupportsValues;
  let zones = FALLBACK_ZONES;
  if (typeof intl.supportedValuesOf === "function") {
    try {
      const supported = intl.supportedValuesOf("timeZone");
      if (supported.length > 0) zones = supported;
    } catch {
      // fall through to the static list
    }
  }
  return zones.includes("UTC") ? zones : ["UTC", ...zones];
}

/** The browser's current zone, or "UTC" if it cannot be resolved. Exported for tests. */
export function resolveDefaultTimezone(): string {
  try {
    return new Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

export function TimezonePicker({
  value,
  onChange,
  disabled,
  invalid,
  id,
}: {
  value: string;
  onChange: (tz: string) => void;
  disabled?: boolean;
  invalid?: boolean;
  id?: string;
}) {
  const options = timezoneOptions();
  // Never drop a stored zone the runtime doesn't enumerate.
  const list = value && !options.includes(value) ? [value, ...options] : options;

  return (
    <Select
      id={id}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      disabled={disabled}
      invalid={invalid}
      aria-label="Time zone"
      className="w-full"
    >
      {list.map((tz) => (
        <option key={tz} value={tz}>
          {tz}
        </option>
      ))}
    </Select>
  );
}
