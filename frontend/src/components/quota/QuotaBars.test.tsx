// QuotaBars rendering: a null limit renders "Unlimited" with no bar; a set limit
// renders a used/limit bar whose fill tone shifts to err once usage exceeds the
// cap. Pure presentational — no query client, no network.
import { cleanup, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it } from "vitest";

import { QuotaBars, allUnlimited } from "@/components/quota/QuotaBars";
import type { QuotaWithUsage } from "@/lib/api/generated/types";

afterEach(() => cleanup());

const base: QuotaWithUsage = {
  scopeType: "project",
  scopeId: "p1",
  limits: {},
  usage: { vcpu: 0, ramMb: 0, diskGb: 0, count: 0 },
  remaining: { vcpu: 0, ramMb: 0, diskGb: 0, count: 0 },
};

/** Count the ProgressBar fills (each rendered with an inline width style). */
function barCount(container: HTMLElement): number {
  return container.querySelectorAll('div[style*="width"]').length;
}

describe("QuotaBars", () => {
  it("renders Unlimited with no bar where the limit is null", () => {
    const { container } = render(
      <QuotaBars
        quota={{ ...base, limits: {}, usage: { vcpu: 6, ramMb: 2048, diskGb: 40, count: 3 } }}
      />,
    );
    // All four dimensions are unlimited → four "Unlimited" labels, zero bars.
    expect(screen.getAllByText(/Unlimited/)).toHaveLength(4);
    expect(barCount(container)).toBe(0);
    // Usage is still shown honestly next to each dimension.
    expect(screen.getByText(/^6 · Unlimited$/)).toBeTruthy();
  });

  it("renders a used/limit bar only for capped dimensions", () => {
    const { container } = render(
      <QuotaBars
        quota={{
          ...base,
          limits: { maxVcpu: 8 },
          usage: { vcpu: 6, ramMb: 0, diskGb: 0, count: 0 },
        }}
      />,
    );
    expect(screen.getByText(/6 \/ 8/)).toBeTruthy();
    // Only vCPU is capped → exactly one bar; the other three stay Unlimited.
    expect(barCount(container)).toBe(1);
    expect(screen.getAllByText(/Unlimited/)).toHaveLength(3);
    const fill = container.querySelector('div[style*="width"]') as HTMLElement;
    expect(fill.className).toContain("bg-accent");
    expect(fill.style.width).toBe("75%");
  });

  it("colors the fill err when usage exceeds the limit", () => {
    const { container } = render(
      <QuotaBars
        quota={{
          ...base,
          limits: { maxVcpu: 8 },
          usage: { vcpu: 10, ramMb: 0, diskGb: 0, count: 0 },
        }}
      />,
    );
    const fill = container.querySelector('div[style*="width"]') as HTMLElement;
    expect(fill.className).toContain("bg-err");
    expect(fill.style.width).toBe("100%"); // clamped
  });

  it("colors the fill warn near the cap (>=80%)", () => {
    const { container } = render(
      <QuotaBars
        quota={{
          ...base,
          limits: { maxCount: 10 },
          usage: { vcpu: 0, ramMb: 0, diskGb: 0, count: 9 },
        }}
      />,
    );
    const fill = container.querySelector('div[style*="width"]') as HTMLElement;
    expect(fill.className).toContain("bg-warn");
  });
});

describe("allUnlimited", () => {
  it("is true only when every limit is null", () => {
    expect(allUnlimited({})).toBe(true);
    expect(allUnlimited({ maxVcpu: 1 })).toBe(false);
    expect(allUnlimited({ maxRamMb: 1024 })).toBe(false);
    expect(allUnlimited({ maxDiskGb: 10 })).toBe(false);
    expect(allUnlimited({ maxCount: 5 })).toBe(false);
  });
});
