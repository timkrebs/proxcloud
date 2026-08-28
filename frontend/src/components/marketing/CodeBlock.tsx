"use client";
// Tabbed code block for the (always-dark) API section: pcctl / Terraform / REST
// snippets with a copy-to-clipboard button that fires a toast. This band is dark
// in both themes, so its surface colors are the design's fixed dark hex values
// (not theme tokens) — the theme only owns light/dark of the marketing chrome,
// not this intentionally-constant dark panel.
import { useState } from "react";

import { CODE } from "./data";
import { useMarketingToast } from "./MarketingRoot";

export function CodeBlock() {
  const [tab, setTab] = useState(0);
  const { push } = useMarketingToast();
  const active = CODE[tab];

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(active.body);
    } catch {
      // Clipboard can be blocked (permissions / insecure context); toast anyway.
    }
    push(`Copied the ${active.label} snippet.`);
  };

  return (
    <div className="min-w-0 overflow-hidden rounded-lg border border-[#3B3A39] bg-[#252423] shadow-[0_12px_32px_rgba(0,0,0,0.45)]">
      <div
        role="tablist"
        aria-label="Code examples"
        className="flex border-b border-[#3B3A39] bg-[#201F1E]"
      >
        {CODE.map((c, i) => {
          const on = i === tab;
          return (
            <button
              key={c.label}
              type="button"
              role="tab"
              aria-selected={on}
              aria-controls="code-panel"
              onClick={() => setTab(i)}
              className={`-mb-px cursor-pointer border-none border-b-2 bg-transparent px-[18px] py-3 text-[14px] hover:text-[#F3F2F1] ${
                on
                  ? "border-[#479EF5] font-semibold text-[#F3F2F1]"
                  : "border-transparent font-normal text-[#A19F9D]"
              }`}
            >
              {c.label}
            </button>
          );
        })}
        <button
          type="button"
          onClick={copy}
          title="Copy snippet"
          aria-label="Copy snippet"
          className="ml-auto flex cursor-pointer items-center gap-[6px] border-none bg-transparent px-4 text-[13px] text-[#A19F9D] hover:text-[#F3F2F1]"
        >
          <svg
            width="13"
            height="13"
            viewBox="0 0 16 16"
            fill="none"
            stroke="currentColor"
            strokeWidth="1.3"
            strokeLinejoin="round"
            aria-hidden
          >
            <path d="M4.5 5.5h7v8h-7zM6.5 5.5v-3h7v8h-2.5" />
          </svg>
          Copy
        </button>
      </div>
      <pre
        id="code-panel"
        role="tabpanel"
        className="m-0 overflow-x-auto p-5 font-mono text-[13px] leading-[1.75] whitespace-pre text-[#DCDCDC]"
      >
        {active.body}
      </pre>
    </div>
  );
}
