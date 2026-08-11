// Sign-in page — design-inventory §3.10 (adapted: username+password, no SSO).
import type { Metadata } from "next";
import Link from "next/link";
import { SignInCard } from "@/components/auth/SignInCard";

export const metadata: Metadata = {
  title: "Sign in — Proxcloud",
};

export default function SignInPage() {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center bg-canvas px-5 py-10">
      <SignInCard />
      {/* §3.10 below-card link (SSO omitted — feature does not exist) */}
      <Link
        href="/"
        className="mt-[18px] text-[12px] text-ink-2 hover:text-ink"
      >
        ← Back to landing page
      </Link>
    </main>
  );
}
