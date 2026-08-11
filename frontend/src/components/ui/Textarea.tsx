"use client";
// Textarea — mono 12.5px, used for cloud-init user data (§4.2, §3.3 step 4).
import type { TextareaHTMLAttributes } from "react";

export interface TextareaProps extends TextareaHTMLAttributes<HTMLTextAreaElement> {
  invalid?: boolean;
}

export function Textarea({ invalid = false, className = "", ...rest }: TextareaProps) {
  const border = invalid ? "border-err" : "border-line-input focus:border-accent";
  return (
    <textarea
      className={`rounded-fluent border bg-card p-[10px] font-mono text-[12.5px] leading-[1.5] outline-none resize-y ${border} ${className}`}
      {...rest}
    />
  );
}
