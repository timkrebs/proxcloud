"use client";
// Checkbox — design-inventory §4.7. Two sizes: 18px (forms, 12px check sw 2)
// and 16px (table rows, 11px check sw 2.2). Checked = accent fill + border
// with a white check; unchecked = white bg, #8A8886 border.
import { Mi } from "@/components/ui/icons";

export interface CheckboxProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  /** "form" = 18×18 (wizard rows), "table" = 16×16 (list-page rows). */
  size?: "form" | "table";
  disabled?: boolean;
  title?: string;
  "aria-label"?: string;
}

export function Checkbox({
  checked,
  onChange,
  size = "form",
  disabled,
  title,
  "aria-label": ariaLabel,
}: CheckboxProps) {
  const form = size === "form";
  return (
    <button
      type="button"
      role="checkbox"
      aria-checked={checked}
      aria-label={ariaLabel}
      title={title}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={`inline-flex shrink-0 cursor-pointer items-center justify-center rounded-fluent border text-white ${
        form ? "h-[18px] w-[18px]" : "h-4 w-4"
      } ${checked ? "border-accent bg-accent" : "border-line-input bg-card"}`}
    >
      {checked ? (
        <Mi name="check" size={form ? 12 : 11} color="currentColor" strokeWidth={form ? 2 : 2.2} />
      ) : null}
    </button>
  );
}
