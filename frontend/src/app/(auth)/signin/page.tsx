// Sign-in page — design-inventory §3.10 (Phase 2 local auth). Fetches
// bootstrap-status on load: first run renders the bootstrap card, otherwise the
// email+password sign-in card (see AuthPanel).
import type { Metadata } from "next";
import Link from "next/link";
import { AuthPanel } from "@/components/auth/AuthPanel";

export const metadata: Metadata = {
  title: "Sign in — Proxcloud",
};

export default function SignInPage() {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center bg-canvas px-5 py-10">
      <AuthPanel />
      <Link href="/" className="mt-[18px] text-[12px] text-ink-2 hover:text-ink">
        ← Back to landing page
      </Link>
    </main>
  );
}
