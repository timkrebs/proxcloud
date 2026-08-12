import type { NextConfig } from "next";

// All /api traffic is proxied to the Go backend so the browser stays on one
// origin (cookies work, no CORS). The console WebSocket is the one exception:
// rewrites don't proxy WS, so it connects to the backend origin directly.
const backendOrigin = process.env.BACKEND_ORIGIN ?? "http://localhost:8080";

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
        // The invitation token is a path segment on /invite/{token}; no-referrer
        // keeps that single-use credential out of the Referer header on any
        // outbound navigation/subresource.
        source: "/invite/:token*",
        headers: [{ key: "Referrer-Policy", value: "no-referrer" }],
      },
    ];
  },
};

export default nextConfig;
