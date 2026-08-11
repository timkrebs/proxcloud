"use client";
// Button — all variants from design-inventory §4.1.
import type { ReactNode } from "react";

export type ButtonVariant =
  | "primary"
  | "primaryCompact"
  | "secondary"
  | "secondaryCompact"
  | "danger"
  | "commandBar"
  | "link";

const BASE = "inline-flex cursor-pointer items-center justify-center whitespace-nowrap select-none";

const VARIANTS: Record<ButtonVariant, string> = {
  // §4.1 Primary: 32px, 0 20px, accent, white 14px/600, radius 2
  primary:
    "h-8 rounded-fluent border border-transparent bg-accent px-5 text-[14px] font-semibold text-white hover:bg-accent-hover active:bg-accent-active",
  // §4.1 Primary (compact): 0 14px, 13px (e.g. "Take snapshot")
  primaryCompact:
    "h-8 rounded-fluent border border-transparent bg-accent px-[14px] text-[13px] font-semibold text-white hover:bg-accent-hover active:bg-accent-active",
  // §4.1 Secondary: 32px, 0 16px, white bg, #8A8886 border, 14px
  secondary:
    "h-8 rounded-fluent border border-line-input bg-card px-4 text-[14px] text-ink enabled:hover:bg-hover disabled:cursor-default disabled:text-ink-3",
  // §4.1 Secondary (compact): 0 14px, 13px (e.g. "Add", "Dismiss all")
  secondaryCompact:
    "h-8 rounded-fluent border border-line-input bg-card px-[14px] text-[13px] text-ink enabled:hover:bg-hover disabled:cursor-default disabled:text-ink-3",
  // §4.1 Danger: accent = #D13438; disabled degrades to #F3F2F1 (§3.14)
  danger:
    "h-8 rounded-fluent border border-transparent bg-err px-5 text-[14px] font-semibold text-white disabled:cursor-not-allowed disabled:bg-hover",
  // §4.1 Command bar: 36px, 0 10px, transparent, 13px, gap 6; disabled #A19F9D
  commandBar:
    "h-9 gap-[6px] border-0 bg-transparent px-[10px] text-[13px] text-ink enabled:hover:bg-hover disabled:cursor-default disabled:text-ink-3",
  // §4.1 Link-button: bare accent text, 13px, hover #005A9E
  link: "gap-[6px] border-0 bg-transparent p-0 text-[13px] text-accent hover:text-accent-active",
};

export interface ButtonProps {
  variant?: ButtonVariant;
  disabled?: boolean;
  onClick?: () => void;
  children: ReactNode;
  title?: string;
  type?: "button" | "submit" | "reset";
  className?: string;
}

export function Button({
  variant = "primary",
  disabled,
  onClick,
  children,
  title,
  type = "button",
  className = "",
}: ButtonProps) {
  return (
    <button
      type={type}
      disabled={disabled}
      onClick={onClick}
      title={title}
      className={`${BASE} ${VARIANTS[variant]} ${className}`}
    >
      {children}
    </button>
  );
}
