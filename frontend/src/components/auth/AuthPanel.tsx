"use client";
// Auth entry switch — decides between first-run bootstrap and normal sign-in.
// The (auth) route group has no query client, so this fetches bootstrap-status
// with a plain resilient apiFetch: if the probe fails (or is slow), we default
// to the sign-in card rather than blocking the user out.

import { useEffect, useState } from "react";
import { BootstrapCard } from "@/components/auth/BootstrapCard";
import { SignInCard } from "@/components/auth/SignInCard";
import { apiFetch } from "@/lib/api/client";
import type { BootstrapStatus } from "@/lib/api/generated/types";

type Mode = "loading" | "bootstrap" | "signin";

export function AuthPanel() {
  const [mode, setMode] = useState<Mode>("loading");

  useEffect(() => {
    let cancelled = false;
    apiFetch<BootstrapStatus>("/api/auth/bootstrap-status")
      .then((s) => {
        if (!cancelled) setMode(s.needsBootstrap ? "bootstrap" : "signin");
      })
      .catch(() => {
        // Resilient default: any failure falls back to the login card.
        if (!cancelled) setMode("signin");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  if (mode === "loading") {
    // Card-shaped skeleton so the layout doesn't jump when the real card lands.
    return (
      <div
        className="w-[400px] max-w-full animate-pulse rounded-fluent border border-line bg-card p-10 shadow-auth"
        aria-hidden
      >
        <div className="mb-6 h-6 w-40 rounded-fluent bg-hover" />
        <div className="mb-3 h-[34px] w-full rounded-fluent bg-hover" />
        <div className="h-8 w-24 rounded-fluent bg-hover" />
      </div>
    );
  }

  if (mode === "bootstrap") {
    return <BootstrapCard onAlreadyBootstrapped={() => setMode("signin")} />;
  }

  return <SignInCard />;
}
