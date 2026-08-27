// Schedule helpers — pure next-run + summary logic. Weekdays are read from the
// candidate calendar dates so the cases don't hardcode a day-of-week constant.
import { describe, expect, it } from "vitest";

import { DAY_LABELS, formatDays, nextRun, parseHhmm, scheduleSummary } from "@/lib/schedule";

const weekdayUTC = (iso: string) => new Date(iso).getUTCDay();

describe("parseHhmm", () => {
  it("accepts valid 24h times", () => {
    expect(parseHhmm("22:00")).toEqual({ h: 22, m: 0 });
    expect(parseHhmm("07:05")).toEqual({ h: 7, m: 5 });
    expect(parseHhmm("00:00")).toEqual({ h: 0, m: 0 });
    expect(parseHhmm("23:59")).toEqual({ h: 23, m: 59 });
  });
  it("rejects malformed times", () => {
    for (const bad of ["24:00", "23:60", "7:5", "1:00", "", "abc", "22:0"]) {
      expect(parseHhmm(bad)).toBeNull();
    }
  });
});

describe("nextRun", () => {
  it("returns today's instant when the time is still ahead (UTC)", () => {
    const now = new Date("2026-08-27T20:00:00Z");
    const wd = weekdayUTC("2026-08-27T00:00:00Z");
    const next = nextRun("22:00", [wd], "UTC", now);
    expect(next?.toISOString()).toBe("2026-08-27T22:00:00.000Z");
  });

  it("rolls to the next matching day when today's time has passed (UTC)", () => {
    const now = new Date("2026-08-27T23:00:00Z");
    const wd = weekdayUTC("2026-08-27T00:00:00Z");
    const next = nextRun("22:00", [wd], "UTC", now);
    expect(next?.toISOString()).toBe("2026-09-03T22:00:00.000Z");
  });

  it("honors the timezone offset (America/New_York, EDT)", () => {
    // 08:00 EDT on Thu Aug 27; 22:00 EDT that day = 02:00Z the next day.
    const now = new Date("2026-08-27T12:00:00Z");
    const wd = weekdayUTC("2026-08-27T00:00:00Z");
    const next = nextRun("22:00", [wd], "America/New_York", now);
    expect(next?.toISOString()).toBe("2026-08-28T02:00:00.000Z");
  });

  it("is null with no days, a bad time, or an unknown timezone", () => {
    const now = new Date("2026-08-27T12:00:00Z");
    expect(nextRun("22:00", [], "UTC", now)).toBeNull();
    expect(nextRun("bad", [1], "UTC", now)).toBeNull();
    expect(nextRun("22:00", [1], "Not/AZone", now)).toBeNull();
  });
});

describe("formatDays", () => {
  it("compacts consecutive runs into ranges", () => {
    expect(formatDays([1, 2, 3, 4, 5])).toBe("Mon–Fri");
    expect(formatDays([0, 6])).toBe("Sun, Sat");
    expect(formatDays([3])).toBe("Wed");
    expect(formatDays([1, 2])).toBe("Mon, Tue");
    expect(formatDays([0, 1, 2, 3, 4, 5, 6])).toBe("Every day");
    expect(formatDays([])).toBe("");
  });
  it("dedupes and sorts", () => {
    expect(formatDays([5, 1, 3, 2, 4, 1])).toBe("Mon–Fri");
  });
  it("labels line up with DAY_LABELS", () => {
    expect(DAY_LABELS[0]).toBe("Sun");
    expect(DAY_LABELS[6]).toBe("Sat");
  });
});

describe("scheduleSummary", () => {
  it("renders the human summary", () => {
    expect(scheduleSummary({ shutdownTime: "22:00", daysOfWeek: [1, 2, 3, 4, 5] })).toBe(
      "Auto-shutdown 22:00 · Mon–Fri",
    );
  });
});
