// Public marketing landing page (route "/"). Statically rendered — no authed
// API, no portal data. The header/footer live in the (marketing) layout; this
// page is just the ordered content sections. Section anchor IDs (#top #services
// #features #how #api #pricing) back the nav and in-page links.
import type { Metadata } from "next";

import {
  ApiSection,
  CtaBand,
  FeatureRows,
  Hero,
  HowItWorks,
  PricingTeaser,
  ProofStrip,
  ServiceGrid,
} from "@/components/marketing/sections";

const TITLE = "Proxcloud — Self-service cloud for Proxmox VE";
const DESCRIPTION =
  "Run your own cloud on your own hardware. Self-service VMs, Kubernetes, databases and networking on Proxmox — with multi-tenancy, quotas, and an Azure-familiar portal.";

export const metadata: Metadata = {
  title: TITLE,
  description: DESCRIPTION,
  alternates: { canonical: "https://portal.proxcloud.io" },
  openGraph: {
    type: "website",
    url: "https://portal.proxcloud.io",
    siteName: "Proxcloud",
    title: TITLE,
    description: DESCRIPTION,
  },
  twitter: {
    card: "summary_large_image",
    title: TITLE,
    description: DESCRIPTION,
  },
};

export default function MarketingLandingPage() {
  return (
    <>
      <Hero />
      <ProofStrip />
      <ServiceGrid />
      <FeatureRows />
      <HowItWorks />
      <ApiSection />
      <PricingTeaser />
      <CtaBand />
    </>
  );
}
