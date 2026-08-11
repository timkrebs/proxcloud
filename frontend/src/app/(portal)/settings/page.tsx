"use client";
// Portal settings — the one placeholder the design itself ships (§3.8).
import { useRouter } from "next/navigation";

import { EmptyState } from "@/components/ui/EmptyState";

export default function SettingsPage() {
  const router = useRouter();
  return (
    <EmptyState
      variant="page"
      icon="gear"
      title="Portal settings"
      body="Theme, language, and default filters arrive in a later iteration. Backend configuration (Proxmox connection, console credentials, pricing) lives in the .env file — see the README."
      cta={{ label: "Go to dashboard", onClick: () => router.push("/dashboard") }}
    />
  );
}
