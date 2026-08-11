// Dashboard — placeholder shell until the cluster endpoints land (milestone 6
// replaces this page). No greeting: there is no user/session data to greet
// with yet. Page frame per design-inventory §3.1 (24px 32px 40px, max 1360).
import type { Metadata } from "next";
import { Card } from "@/components/ui/Card";
import { Mi } from "@/components/ui/icons";

export const metadata: Metadata = {
  title: "Dashboard — Proxcloud",
};

export default function DashboardPage() {
  return (
    <div className="max-w-[1360px] px-8 pt-6 pb-10">
      <Card className="flex items-start gap-[10px] p-4">
        {/* hex allowed only inside icon color props (§2.7 err accent) */}
        <Mi name="warn" size={16} color="#D13438" />
        <div>
          <div className="text-[14px] font-semibold">Dashboard not wired yet</div>
          <div className="mt-[2px] text-[13px] text-ink-2">
            Backend cluster endpoints arrive in the next milestone.
          </div>
        </div>
      </Card>
    </div>
  );
}
