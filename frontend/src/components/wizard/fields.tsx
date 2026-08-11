"use client";
// Wizard form primitives — design §3.3 anatomy: 220px label + 300px control.
import { Mi } from "@/components/ui/icons";

export function FormRow({
  label,
  required,
  help,
  error,
  children,
}: {
  label: string;
  required?: boolean;
  help?: string;
  error?: string;
  children: React.ReactNode;
}) {
  return (
    <div className="mb-[14px] flex items-start">
      <label className="flex w-[220px] flex-none items-center gap-1 pt-[7px] text-[14px]">
        {label}
        {help ? (
          <span title={help} className="inline-flex">
            <Mi name="info" size={13} color="var(--color-ink-2)" />
          </span>
        ) : null}
        {required ? <span className="text-err-text">*</span> : null}
      </label>
      <div>
        {children}
        {error ? <p className="mt-1 max-w-[300px] text-[12px] text-err-text">{error}</p> : null}
      </div>
    </div>
  );
}

export function SectionHeading({ children, caption }: { children: string; caption?: string }) {
  return (
    <div className="mt-[22px] first:mt-0">
      <h2 className="text-[16px] font-semibold">{children}</h2>
      {caption ? <p className="mt-1 text-[12px] text-ink-2">{caption}</p> : null}
      <div className="mt-2 mb-[14px] h-px bg-line" />
    </div>
  );
}
