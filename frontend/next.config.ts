import type { NextConfig } from "next";

// All /api traffic is proxied to the Go backend so the browser stays on one
// origin (cookies work, no CORS). The console WebSocket is the one exception:
// rewrites don't proxy WS, so it connects to the backend origin directly.
const backendOrigin = process.env.BACKEND_ORIGIN ?? "http://localhost:8080";

const nextConfig: NextConfig = {
  async rewrites() {
    return [
      {
        source: "/api/:path*",
        destination: `${backendOrigin}/api/:path*`,
      },
    ];
  },
};

export default nextConfig;
