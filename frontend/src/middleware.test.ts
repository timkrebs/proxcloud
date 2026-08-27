// Middleware decision logic — the authed-cookie → /dashboard redirect only
// fires on marketing paths; portal / auth / api / static paths pass through.
import { describe, expect, it } from "vitest";

import { isMarketingPath, MARKETING_PATHS, shouldRedirectToDashboard } from "./middleware";

describe("isMarketingPath", () => {
  it("matches exactly the public marketing paths", () => {
    for (const p of MARKETING_PATHS) {
      expect(isMarketingPath(p)).toBe(true);
    }
  });

  it("does not match portal, auth, api, invite, or static paths", () => {
    for (const p of [
      "/dashboard",
      "/signin",
      "/api/me",
      "/api/events/metrics",
      "/invite/abc123",
      "/resources",
      "/products/extra",
      "/_next/static/chunk.js",
      "/favicon.ico",
    ]) {
      expect(isMarketingPath(p)).toBe(false);
    }
  });
});

describe("shouldRedirectToDashboard", () => {
  it("redirects an authenticated visitor on a marketing path", () => {
    expect(shouldRedirectToDashboard("/", true)).toBe(true);
    expect(shouldRedirectToDashboard("/pricing", true)).toBe(true);
    expect(shouldRedirectToDashboard("/docs", true)).toBe(true);
  });

  it("passes through when there is no session cookie", () => {
    expect(shouldRedirectToDashboard("/", false)).toBe(false);
    expect(shouldRedirectToDashboard("/pricing", false)).toBe(false);
  });

  it("never redirects a non-marketing path, even when authenticated", () => {
    expect(shouldRedirectToDashboard("/dashboard", true)).toBe(false);
    expect(shouldRedirectToDashboard("/signin", true)).toBe(false);
    expect(shouldRedirectToDashboard("/api/me", true)).toBe(false);
    expect(shouldRedirectToDashboard("/invite/abc", true)).toBe(false);
  });
});
