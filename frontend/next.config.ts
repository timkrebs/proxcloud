import type { NextConfig } from "next";

// All /api traffic is proxied to the Go backend so the browser stays on one
// origin (cookies work, no CORS). The console WebSocket is the one exception:
// rewrites don't proxy WS, so it connects to the backend origin directly.
const backendOrigin = process.env.BACKEND_ORIGIN ?? "http://localhost:8080";

// Content-Security-Policy for the portal (ADR-0017). The app is same-origin (the
// browser hits the portal host; Next proxies /api and Caddy proxies the console
// WS), so connect-src is locked to 'self' (covers same-origin wss) — this is the
// key control: even if a script injection occurred, it could not exfiltrate to a
// foreign origin. frame-ancestors 'none' blocks clickjacking of the VM-destroying
// controls; object/base/form are locked down.
//
// script-src/style-src keep 'unsafe-inline' because Next's App Router ships inline
// hydration/bootstrap scripts and inline styles; tightening to a nonce-based CSP
// is a tracked follow-up (ADR-0017 §Residual). connect-src stays strict either way.
//
// Local dev (`next dev`) needs two relaxations that must never ship:
// - HMR/Fast Refresh evaluates modules via eval() → script-src needs 'unsafe-eval'.
// - The console WebSocket connects to the backend origin directly (rewrites don't
//   proxy WS; the root docker-compose sets NEXT_PUBLIC_BACKEND_WS_ORIGIN to
//   ws://localhost:8080) → connect-src must allow that cross-origin target.
// The branch is decided at config-eval time: `next build`/`next start` run with
// NODE_ENV=production, so the shipped CSP is exactly the strict base below.
// Strict equality: an unusual/typo'd NODE_ENV must yield the PROD policy, not
// the loosened dev one (fail closed).
const isDev = process.env.NODE_ENV === "development";

// Compose one CSP directive: base sources always, devOnly sources appended in dev.
const directive = (name: string, base: string[], devOnly: string[] = []): string =>
  [name, ...base, ...(isDev ? devOnly : [])].join(" ");

const csp = [
  directive("default-src", ["'self'"]),
  directive("script-src", ["'self'", "'unsafe-inline'"], ["'unsafe-eval'"]),
  directive("style-src", ["'self'", "'unsafe-inline'"]),
  directive("img-src", ["'self'", "data:"]),
  directive("font-src", ["'self'"]),
  directive("connect-src", ["'self'"], ["ws://localhost:8080", "http://localhost:8080"]),
  directive("frame-ancestors", ["'none'"]),
  directive("base-uri", ["'self'"]),
  directive("object-src", ["'none'"]),
  directive("form-action", ["'self'"]),
].join("; ");

// Applied to every response; /invite tightens Referrer-Policy further below.
const securityHeaders = [
  { key: "Content-Security-Policy", value: csp },
  // HSTS without `preload`: an accidental preload-list submission of a homelab
  // domain is irreversible for months. Mode-B (*.proxcloud.lab internal-CA)
  // operators may want to drop includeSubDomains too if sibling lab hosts must
  // stay reachable over plain HTTP; we ship it on.
  { key: "Strict-Transport-Security", value: "max-age=63072000; includeSubDomains" },
  { key: "X-Content-Type-Options", value: "nosniff" },
  { key: "X-Frame-Options", value: "DENY" },
  { key: "Referrer-Policy", value: "strict-origin-when-cross-origin" },
  { key: "Permissions-Policy", value: "camera=(), microphone=(), geolocation=(), interest-cohort=()" },
];

const nextConfig: NextConfig = {
  // Emit a self-contained server bundle (.next/standalone) so the production
  // image ships only the traced runtime — no full node_modules. See
  // frontend/Dockerfile prod stage.
  output: "standalone",
  // No next/image usage anywhere; kills the /_next/image optimizer endpoint (GHSA-2xp9-vwfh-vxw4 surface).
  images: { unoptimized: true },
  async rewrites() {
    return [
      {
        source: "/api/:path*",
        destination: `${backendOrigin}/api/:path*`,
      },
    ];
  },
  async headers() {
    return [
      {
        // Security headers on every response (CSP, HSTS, framing, nosniff, ...).
        source: "/:path*",
        headers: securityHeaders,
      },
      {
        // The invitation token is a path segment on /invite/{token}; no-referrer
        // keeps that single-use credential out of the Referer header on any
        // outbound navigation/subresource (overrides the strict-origin default).
        source: "/invite/:token*",
        headers: [{ key: "Referrer-Policy", value: "no-referrer" }],
      },
    ];
  },
};

export default nextConfig;
