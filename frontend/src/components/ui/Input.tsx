"use client";
// Text input — standard + underline (auth) variants per design-inventory §4.2.
import type { InputHTMLAttributes } from "react";

export interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  variant?: "standard" | "underline";
  invalid?: boolean;
}

export function Input({
  variant = "standard",
  invalid = false,
  className = "",
  ...rest
}: InputProps) {
  const shape =
    variant === "underline"
      ? // §4.2 underline (auth only): 34px, border-bottom only, 15px, transparent bg
        "h-[34px] rounded-none border-0 border-b bg-transparent px-[2px] text-[15px] outline-none"
      : // §4.2 standard: 32px, 1px border, radius 2, 0 8px, 14px, white bg
        "h-8 rounded-fluent border bg-card px-2 text-[14px] outline-none";
  const border = invalid
    ? "border-err" // §4.2 invalid: border #D13438
    : "border-line-input focus:border-accent";
  return <input className={`${shape} ${border} ${className}`} {...rest} />;
}
