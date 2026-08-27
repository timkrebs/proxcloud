// Schedule-editor validation — pure, mirroring the backend 400s (HH:MM format,
// ≥1 day, grace 1–300), and the scope rule that opt-out is resource-only.
import { describe, expect, it } from "vitest";

import {
  defaultScheduleForm,
  scheduleToForm,
  validateScheduleForm,
  type ScheduleFormValues,
} from "@/components/schedule/ScheduleEditor";
import type { Schedule } from "@/lib/api/generated/types";

const base = (): ScheduleFormValues => defaultScheduleForm("Europe/Berlin");

describe("validateScheduleForm", () => {
  it("accepts the defaults and emits a clean resource body", () => {
    const { errors, body } = validateScheduleForm(base(), "resource");
    expect(errors).toEqual({});
    expect(body).toEqual({
      shutdownTime: "22:00",
      autoStartTime: undefined,
      daysOfWeek: [1, 2, 3, 4, 5],
      timezone: "Europe/Berlin",
      graceSeconds: 120,
      enabled: true,
      optOut: false,
    });
  });

  it("omits optOut on project scope", () => {
    const { body } = validateScheduleForm(base(), "project");
    expect(body).toBeDefined();
    expect("optOut" in body!).toBe(false);
  });

  it("includes an enabled auto-start time and drops it when disabled", () => {
    const on = validateScheduleForm({ ...base(), autoStartEnabled: true, autoStartTime: "07:30" }, "resource");
    expect(on.body?.autoStartTime).toBe("07:30");
    const off = validateScheduleForm({ ...base(), autoStartEnabled: false, autoStartTime: "07:30" }, "resource");
    expect(off.body?.autoStartTime).toBeUndefined();
  });

  const badTimes: [Partial<ScheduleFormValues>, keyof ReturnType<typeof validateScheduleForm>["errors"]][] = [
    [{ shutdownTime: "25:00" }, "shutdownTime"],
    [{ shutdownTime: "" }, "shutdownTime"],
    [{ autoStartEnabled: true, autoStartTime: "9:9" }, "autoStartTime"],
    [{ daysOfWeek: [] }, "daysOfWeek"],
    [{ timezone: "  " }, "timezone"],
    [{ graceSeconds: "0" }, "graceSeconds"],
    [{ graceSeconds: "301" }, "graceSeconds"],
    [{ graceSeconds: "abc" }, "graceSeconds"],
    [{ graceSeconds: "12.5" }, "graceSeconds"],
  ];
  it.each(badTimes)("flags %j on %s and produces no body", (patch, field) => {
    const { errors, body } = validateScheduleForm({ ...base(), ...patch }, "resource");
    expect(errors[field]).toBeTruthy();
    expect(body).toBeUndefined();
  });

  it("allows grace at the 1 and 300 bounds", () => {
    expect(validateScheduleForm({ ...base(), graceSeconds: "1" }, "resource").body?.graceSeconds).toBe(1);
    expect(validateScheduleForm({ ...base(), graceSeconds: "300" }, "resource").body?.graceSeconds).toBe(300);
  });

  it("dedupes and sorts days in the body", () => {
    const { body } = validateScheduleForm({ ...base(), daysOfWeek: [5, 1, 1, 3] }, "resource");
    expect(body?.daysOfWeek).toEqual([1, 3, 5]);
  });
});

describe("scheduleToForm", () => {
  const stored: Schedule = {
    id: "s1",
    scope: "resource",
    tenantId: "t1",
    projectId: "p1",
    vmid: 101,
    shutdownTime: "23:15",
    autoStartTime: "06:45",
    daysOfWeek: [6, 0],
    timezone: "America/New_York",
    graceSeconds: 90,
    enabled: false,
    optOut: true,
    createdAt: "2026-08-01T00:00:00Z",
    updatedAt: "2026-08-01T00:00:00Z",
  };

  it("round-trips a stored schedule into a valid form + body", () => {
    const form = scheduleToForm(stored);
    expect(form.autoStartEnabled).toBe(true);
    expect(form.daysOfWeek).toEqual([0, 6]);
    expect(form.graceSeconds).toBe("90");
    const { body } = validateScheduleForm(form, "resource");
    expect(body).toEqual({
      shutdownTime: "23:15",
      autoStartTime: "06:45",
      daysOfWeek: [0, 6],
      timezone: "America/New_York",
      graceSeconds: 90,
      enabled: false,
      optOut: true,
    });
  });

  it("treats a missing auto-start as disabled", () => {
    const form = scheduleToForm({ ...stored, autoStartTime: undefined });
    expect(form.autoStartEnabled).toBe(false);
  });
});
