import type { Metadata } from "next";
import "./globals.css";

// Font: the design's Segoe UI system stack comes from globals.css (§1.1) —
// no webfont imports.
export const metadata: Metadata = {
  title: "Proxcloud",
  description: "Self-service cloud console for Proxmox VE",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="en">
      <body className="antialiased">{children}</body>
    </html>
  );
}
