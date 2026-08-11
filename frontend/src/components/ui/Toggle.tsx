"use client";
// Toggle switch — 40×20 track, radius 10, 12px knob per design-inventory §4.7.
// on: accent track + white knob at left 23px; off: white track, #8A8886 border,
// #605E5C knob at left 3px; left/background transition .15s.

export interface ToggleProps {
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
  title?: string;
  "aria-label"?: string;
}

export function Toggle({ checked, onChange, disabled, title, "aria-label": ariaLabel }: ToggleProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={ariaLabel}
      title={title}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={`relative h-5 w-10 shrink-0 cursor-pointer rounded-[10px] border transition-colors duration-150 ${
        checked ? "border-accent bg-accent" : "border-line-input bg-card"
      }`}
    >
      <span
        className={`absolute top-[3px] h-3 w-3 rounded-full transition-[left,background-color] duration-150 ${
          checked ? "left-[23px] bg-white" : "left-[3px] bg-ink-2"
        }`}
      />
    </button>
  );
}
