// Portal shell — design-inventory §2.1–§2.5. All data-connected chrome
// (user chip, bell, panes, palette search, SSE) lives in PortalChrome,
// which must render inside <Providers> to reach the query client.
import { Providers } from "@/components/ui/Providers";
import PortalChrome from "@/components/chrome/PortalChrome";

export default function PortalLayout({ children }: { children: React.ReactNode }) {
  return (
    <Providers>
      <PortalChrome>{children}</PortalChrome>
    </Providers>
  );
}
