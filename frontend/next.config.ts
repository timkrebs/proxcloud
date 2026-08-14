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
const csp = [
  "default-src 'self'",
  "script-src 'self' 'unsafe-inline'",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data:",
  "font-src 'self'",
  "connect-src 'self'",
  "frame-ancestors 'none'",
  "base-uri 'self'",
  "object-src 'none'",
  "form-action 'self'",
].join("; ");

// Applied to every response; /invite tightens Referrer-Policy further below.
const securityHeaders = [
  { key: "Content-Security-Policy", value: csp },
  { key: "Strict-Transport-Security", value: "max-age=63072000; includeSubDomains; preload" },
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
