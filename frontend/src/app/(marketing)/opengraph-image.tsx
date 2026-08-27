// Generated Open Graph / Twitter card image for the marketing landing (and the
// marketing tree). Rendered at build/request time by Next's ImageResponse
// (Satori) — no raster asset in the repo. NOTE: ImageResponse JSX is Satori, not
// the DOM, so inline `style` objects are required here (the app's "no inline
// style" rule is a DOM-component rule and does not apply to this renderer).
import { ImageResponse } from "next/og";

export const size = { width: 1200, height: 630 };
export const contentType = "image/png";
export const alt = "Proxcloud — Self-service cloud for Proxmox VE";

export default function OpengraphImage() {
  return new ImageResponse(
    (
      <div
        style={{
          width: "100%",
          height: "100%",
          display: "flex",
          flexDirection: "column",
          justifyContent: "space-between",
          background: "#faf9f8",
          padding: "80px",
          fontFamily: "sans-serif",
        }}
      >
        <div style={{ display: "flex", alignItems: "center", gap: "18px" }}>
          <svg width="56" height="56" viewBox="0 0 20 20">
            <path d="M10 1.5 18 6v8l-8 4.5L2 14V6z" fill="#0078D4" />
            <path d="M10 1.5 18 6l-8 4.5L2 6z" fill="#50E6FF" />
            <path d="M10 10.5V19L2 14V6z" fill="#005BA1" />
          </svg>
          <span style={{ fontSize: 40, fontWeight: 600, color: "#323130" }}>Proxcloud</span>
        </div>
        <div style={{ display: "flex", flexDirection: "column", gap: "24px" }}>
          <div
            style={{
              fontSize: 68,
              fontWeight: 700,
              color: "#323130",
              lineHeight: 1.05,
              letterSpacing: "-2px",
              maxWidth: "960px",
            }}
          >
            Your own cloud, on your own hardware.
          </div>
          <div style={{ fontSize: 30, color: "#605e5c", maxWidth: "900px", lineHeight: 1.4 }}>
            Self-service VMs, Kubernetes, databases and networking on Proxmox — with
            multi-tenancy, quotas, and an Azure-familiar portal.
          </div>
        </div>
        <div style={{ display: "flex", alignItems: "center", gap: "12px" }}>
          <div style={{ height: "8px", width: "56px", background: "#0078d4", borderRadius: "4px" }} />
          <span style={{ fontSize: 26, color: "#a19f9d" }}>portal.proxcloud.io</span>
        </div>
      </div>
    ),
    { ...size },
  );
}
