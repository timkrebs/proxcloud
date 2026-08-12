// Auth input validation tests — these mirror the backend's email/password
// rules (email shape, password >= 12), so every case has a server-side twin.
import { describe, expect, it } from "vitest";

import { isValidEmail, isValidPassword, MIN_PASSWORD_LEN } from "@/lib/auth/validation";

describe("isValidEmail", () => {
  it("accepts well-formed addresses", () => {
    expect(isValidEmail("admin@example.com")).toBe(true);
    expect(isValidEmail("a.b-c+tag@sub.example.co")).toBe(true);
  });

  it("trims surrounding whitespace before checking", () => {
    expect(isValidEmail("  admin@example.com  ")).toBe(true);
  });

  it("rejects malformed or empty input", () => {
    for (const bad of ["", "admin", "admin@", "@example.com", "admin@example", "a b@example.com"]) {
      expect(isValidEmail(bad)).toBe(false);
    }
  });
});

describe("isValidPassword", () => {
  it("requires at least the minimum length", () => {
    expect(isValidPassword("x".repeat(MIN_PASSWORD_LEN - 1))).toBe(false);
    expect(isValidPassword("x".repeat(MIN_PASSWORD_LEN))).toBe(true);
    expect(isValidPassword("x".repeat(MIN_PASSWORD_LEN + 5))).toBe(true);
  });

  it("rejects an empty password", () => {
    expect(isValidPassword("")).toBe(false);
  });
});
