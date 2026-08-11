"use client";
// Select — identical to the standard input but with 0 6px padding (§4.2).
import type { SelectHTMLAttributes } from "react";

export interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  invalid?: boolean;
}

export function Select({ invalid = false, className = "", children, ...rest }: SelectProps) {
  const border = invalid ? "border-err" : "border-line-input focus:border-accent";
  return (
    <select
      className={`h-8 rounded-fluent border bg-card px-[6px] text-[14px] outline-none ${border} ${className}`}
      {...rest}
    >
      {children}
    </select>
  );
}
