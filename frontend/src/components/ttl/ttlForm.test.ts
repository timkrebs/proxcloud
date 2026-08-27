// TTL-editor validation — pure, mirroring the backend 400s (ttlSeconds > 0 and
// ≤ the project max) and the delete-confirmation gate (a delete TTL requires the
// typed guest name). Plus the policy-flyout validation (max required, default
// optional and ≤ max).
import { describe, expect, it } from "vitest";

import {
  HOUR_SECONDS,
  TTL_PRESETS,
  defaultTtlForm,
  secondsToPreset,
  ttlFormSeconds,
  ttlToForm,
  validateTtlForm,
  type TtlFormValues,
} from "@/components/ttl/TtlEditor";
import { validateTtlPolicyForm } from "@/components/ttl/ProjectTtlPanel";
import type { Ttl } from "@/lib/api/generated/types";

const DAY = 24 * HOUR_SECONDS;
const MAX = 30 * DAY; // 30 days
const GUEST = "web-01";

const base = (over: Partial<TtlFormValues> = {}): TtlFormValues => ({
  preset: "24h",
  customHours: "",
  action: "stop",
  confirmName: "",
  ...over,
});

describe("ttlFormSeconds", () => {
  it("resolves each preset to its seconds", () => {
    expect(ttlFormSeconds(base({ preset: "24h" }))).toBe(DAY);
    expect(ttlFormSeconds(base({ preset: "48h" }))).toBe(2 * DAY);
    expect(ttlFormSeconds(base({ preset: "7d" }))).toBe(7 * DAY);
    expect(ttlFormSeconds(base({ preset: "30d" }))).toBe(30 * DAY);
  });

  it("resolves custom whole hours and rejects non-positive / non-integer", () => {
    expect(ttlFormSeconds(base({ preset: "custom", customHours: "36" }))).toBe(36 * HOUR_SECONDS);
    expect(ttlFormSeconds(base({ preset: "custom", customHours: "0" }))).toBeNull();
    expect(ttlFormSeconds(base({ preset: "custom", customHours: "-4" }))).toBeNull();
    expect(ttlFormSeconds(base({ preset: "custom", customHours: "1.5" }))).toBeNull();
    expect(ttlFormSeconds(base({ preset: "custom", customHours: "" }))).toBeNull();
    expect(ttlFormSeconds(base({ preset: "custom", customHours: "abc" }))).toBeNull();
  });
});

describe("secondsToPreset", () => {
  it("maps an exact preset match", () => {
    expect(secondsToPreset(7 * DAY)).toEqual({ preset: "7d", customHours: "" });
  });
  it("falls back to custom whole hours (rounding up)", () => {
    expect(secondsToPreset(36 * HOUR_SECONDS)).toEqual({ preset: "custom", customHours: "36" });
    expect(secondsToPreset(90 * 60)).toEqual({ preset: "custom", customHours: "2" }); // 1.5h → 2h
  });
});

describe("validateTtlForm — duration / max", () => {
  it("accepts a preset within max and emits a stop body without confirmName", () => {
    const { errors, body } = validateTtlForm(base({ preset: "7d" }), { maxTtlSeconds: MAX, guestName: GUEST });
    expect(errors).toEqual({});
    expect(body).toEqual({ action: "stop", ttlSeconds: 7 * DAY });
    expect(body && "confirmName" in body).toBe(false);
  });

  it("rejects a custom duration above the project max, with no body", () => {
    const over = String((MAX + HOUR_SECONDS) / HOUR_SECONDS); // one hour over
    const { errors, body } = validateTtlForm(base({ preset: "custom", customHours: over }), {
      maxTtlSeconds: MAX,
      guestName: GUEST,
    });
    expect(errors.duration).toBeTruthy();
    expect(body).toBeUndefined();
  });

  it("rejects an invalid custom value, with no body", () => {
    const { errors, body } = validateTtlForm(base({ preset: "custom", customHours: "0" }), {
      maxTtlSeconds: MAX,
      guestName: GUEST,
    });
    expect(errors.duration).toBeTruthy();
    expect(body).toBeUndefined();
  });

  it("accepts a duration exactly at the max boundary", () => {
    const atMax = String(MAX / HOUR_SECONDS);
    const { errors, body } = validateTtlForm(base({ preset: "custom", customHours: atMax }), {
      maxTtlSeconds: MAX,
      guestName: GUEST,
    });
    expect(errors.duration).toBeUndefined();
    expect(body?.ttlSeconds).toBe(MAX);
  });
});

describe("validateTtlForm — delete-confirmation gating", () => {
  it("blocks a delete TTL until the guest name is typed exactly", () => {
    const empty = validateTtlForm(base({ action: "delete", confirmName: "" }), {
      maxTtlSeconds: MAX,
      guestName: GUEST,
    });
    expect(empty.errors.confirmName).toBeTruthy();
    expect(empty.body).toBeUndefined();

    const wrong = validateTtlForm(base({ action: "delete", confirmName: "web-0" }), {
      maxTtlSeconds: MAX,
      guestName: GUEST,
    });
    expect(wrong.errors.confirmName).toBeTruthy();
    expect(wrong.body).toBeUndefined();
  });

  it("allows a delete TTL with a matching name and carries confirmName in the body", () => {
    const { errors, body } = validateTtlForm(base({ action: "delete", confirmName: GUEST }), {
      maxTtlSeconds: MAX,
      guestName: GUEST,
    });
    expect(errors).toEqual({});
    expect(body).toEqual({ action: "delete", ttlSeconds: DAY, confirmName: GUEST });
  });

  it("does not require confirmName for a stop TTL", () => {
    const { errors, body } = validateTtlForm(base({ action: "stop", confirmName: "" }), {
      maxTtlSeconds: MAX,
      guestName: GUEST,
    });
    expect(errors.confirmName).toBeUndefined();
    expect(body).toBeDefined();
  });
});

describe("defaultTtlForm / ttlToForm", () => {
  it("prefills the project default when set, else falls back to 24h/stop", () => {
    expect(defaultTtlForm(7 * DAY)).toMatchObject({ preset: "7d", action: "stop" });
    expect(defaultTtlForm(null)).toMatchObject({ preset: "24h", action: "stop" });
    expect(defaultTtlForm(undefined)).toMatchObject({ preset: "24h", action: "stop" });
  });

  it("round-trips a stored TTL into a valid form + body", () => {
    const stored: Ttl = {
      id: "ttl1",
      tenantId: "t1",
      projectId: "p1",
      vmid: 101,
      expiresAt: "2026-09-01T00:00:00Z",
      action: "delete",
      warned24h: false,
      warned1h: false,
      originalDurationSeconds: 7 * DAY,
      createdAt: "2026-08-01T00:00:00Z",
      updatedAt: "2026-08-01T00:00:00Z",
    };
    const form = ttlToForm(stored);
    expect(form.preset).toBe("7d");
    expect(form.action).toBe("delete");
    // A round-tripped delete form still needs the typed name before it validates.
    expect(validateTtlForm(form, { maxTtlSeconds: MAX, guestName: GUEST }).body).toBeUndefined();
    expect(
      validateTtlForm({ ...form, confirmName: GUEST }, { maxTtlSeconds: MAX, guestName: GUEST }).body,
    ).toEqual({ action: "delete", ttlSeconds: 7 * DAY, confirmName: GUEST });
  });

  it("exposes ascending presets", () => {
    const secs = TTL_PRESETS.map((p) => p.seconds);
    expect(secs).toEqual([...secs].sort((a, b) => a - b));
  });
});

describe("validateTtlPolicyForm", () => {
  it("accepts a max with an optional default within it", () => {
    const { errors, body } = validateTtlPolicyForm("720", "24"); // 30d max, 24h default
    expect(errors).toEqual({});
    expect(body).toEqual({ maxTtlSeconds: 720 * HOUR_SECONDS, defaultTtlSeconds: 24 * HOUR_SECONDS });
  });

  it("treats a blank default as no default", () => {
    const { body } = validateTtlPolicyForm("48", "");
    expect(body).toEqual({ maxTtlSeconds: 48 * HOUR_SECONDS, defaultTtlSeconds: undefined });
  });

  it("rejects a non-positive max", () => {
    expect(validateTtlPolicyForm("0", "").errors.maxHours).toBeTruthy();
    expect(validateTtlPolicyForm("-1", "").errors.maxHours).toBeTruthy();
    expect(validateTtlPolicyForm("1.5", "").errors.maxHours).toBeTruthy();
  });

  it("rejects a default above the max", () => {
    const { errors, body } = validateTtlPolicyForm("24", "48");
    expect(errors.defaultHours).toBeTruthy();
    expect(body).toBeUndefined();
  });
});
