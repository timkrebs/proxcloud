// Formatting layer tests — wire numbers in, design display strings out.
import { describe, expect, it } from "vitest";

import {
  formatBytes,
  formatBytesPair,
  formatMoney,
  formatPct,
  formatRate,
  formatUptime,
  relativeTime,
} from "@/lib/format";

describe("formatBytes", () => {
  const cases: [number, string][] = [
    [0, "0 B"],
    [512, "512 B"],
    [1023, "1023 B"],
    [1024, "1.0 KiB"],
    [8 * 2 ** 30, "8.0 GiB"],
    [1.2 * 2 ** 40, "1.2 TiB"],
  ];
  it.each(cases)("%d → %s", (n, want) => {
    expect(formatBytes(n)).toBe(want);
  });
});

it("formatBytesPair shares the total's unit", () => {
  expect(formatBytesPair(96 * 2 ** 30, 128 * 2 ** 30)).toBe("96.0 / 128.0 GiB");
});

describe("formatPct", () => {
  it("clamps and rounds", () => {
    expect(formatPct(12.4)).toBe("12%");
    expect(formatPct(150)).toBe("100%");
    expect(formatPct(-5)).toBe("0%");
    expect(formatPct(NaN)).toBe("—");
  });
});

describe("formatRate", () => {
  it("uses decimal units like the design", () => {
    expect(formatRate(3_200_000)).toBe("3.2 MB/s");
    expect(formatRate(500)).toBe("500 B/s");
    expect(formatRate(-1)).toBe("—");
  });
});

describe("formatUptime", () => {
  const cases: [number, string][] = [
    [0, "—"],
    [180, "3m"],
    [3 * 3600 + 32 * 60, "3h 32m"],
    [12 * 86400 + 4 * 3600, "12d 4h"],
  ];
  it.each(cases)("%d → %s", (sec, want) => {
    expect(formatUptime(sec)).toBe(want);
  });
});

describe("relativeTime", () => {
  const now = new Date("2026-08-11T12:00:00Z");
  const cases: [string, string][] = [
    ["2026-08-11T11:59:30Z", "Just now"],
    ["2026-08-11T11:55:00Z", "5 min ago"],
    ["2026-08-11T10:00:00Z", "2 h ago"],
    ["2026-08-10T09:00:00Z", "Yesterday"],
    ["2026-08-08T12:00:00Z", "3 d ago"],
  ];
  it.each(cases)("%s → %s", (iso, want) => {
    expect(relativeTime(iso, now)).toBe(want);
  });
  it("falls back to absolute beyond a week", () => {
    expect(relativeTime("2026-03-03T14:22:00Z", now)).toMatch(/^Mar 3, 2026/);
  });
});

describe("formatMoney", () => {
  it("formats known currencies with symbols", () => {
    expect(formatMoney(36, "EUR")).toBe("€36.00");
    expect(formatMoney(412.375, "USD")).toBe("$412.38");
    expect(formatMoney(10, "SEK")).toBe("10.00 SEK");
  });
});
