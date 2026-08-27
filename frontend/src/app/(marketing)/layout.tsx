// Marketing layout — the public shell shared by the landing page and every
// marketing sub-page. It mounts the client theme/toast controller (Marketing
// Root) and the shared header/footer. It deliberately does NOT import the
// portal's <Providers> or PortalChrome and makes no authenticated API calls;
// these routes are public + static (ADR-0021). Authenticated visitors are
// bounced to /dashboard by src/middleware.ts before this ever renders.
import type { Metadata } from "next";
import type { ReactNode } from "react";

import { MarketingFooter } from "@/components/marketing/MarketingFooter";
import { MarketingHeader } from "@/components/marketing/MarketingHeader";
import { MarketingRoot } from "@/components/marketing/MarketingRoot";

// Base URL for resolving the generated opengraph-image (and any relative
// metadata URL) to an absolute one on the public marketing origin.
export const metadata: Metadata = {
  metadataBase: new URL("https://portal.proxcloud.io"),
};

export default function MarketingLayout({ children }: { children: ReactNode }) {
  return (
    <MarketingRoot>
      <MarketingHeader />
      <main className="flex-1">{children}</main>
      <MarketingFooter />
    </MarketingRoot>
  );
}
