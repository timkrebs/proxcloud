"use client";
// Two-step sign-in card — design-inventory §3.10, adapted to the
// single-admin backend: username + password against POST /api/auth/login.
// No SSO button (feature does not exist) and no "Request a tenant" link.

import { useState } from "react";
import { useRouter } from "next/navigation";
import { BrandLogo, Mi } from "@/components/ui/icons";
import { apiFetch, ApiError } from "@/lib/api/client";

// §3.10 underline input: h 34, border-bottom #8A8886 → accent on focus, 15px
const UNDERLINE_INPUT =
  "h-[34px] w-full border-0 border-b border-line-input bg-transparent px-[2px] text-[15px] text-ink outline-none placeholder:text-ink-3 focus:border-b-accent";

// §4.1 primary button (32px, 14px/600, radius 2); px set per step below
const PRIMARY_BTN =
  "inline-flex h-8 cursor-pointer items-center justify-center whitespace-nowrap rounded-fluent border border-transparent bg-accent text-[14px] font-semibold text-white select-none enabled:hover:bg-accent-hover enabled:active:bg-accent-active disabled:cursor-default";

export function SignInCard() {
  const router = useRouter();
  const [step, setStep] = useState<"username" | "password">("username");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);

  function handleNext(e: React.FormEvent) {
    e.preventDefault();
    if (!username.trim()) {
      setError("Enter your username.");
      return;
    }
    setError("");
    setStep("password");
  }

  function handleBack() {
    setStep("username");
    setPassword("");
    setError("");
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!password) {
      setError("Enter your password.");
      return;
    }
    setError("");
    setPending(true);
    try {
      await apiFetch<void>("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({ username, password }),
      });
      router.push("/dashboard");
    } catch (err) {
      setError(
        err instanceof ApiError
          ? err.message
          : "Unable to reach the server. Try again."
      );
      setPending(false);
    }
  }

  return (
    <div className="w-[400px] max-w-full rounded-fluent border border-line bg-card p-10 shadow-auth">
      {/* Brand row — 24px logo + wordmark 16px/600, gap 9, mb 22 */}
      <div className="mb-[22px] flex items-center gap-[9px]">
        <BrandLogo size={24} />
        <span className="text-[16px] font-semibold text-ink">Proxcloud</span>
      </div>

      {step === "username" ? (
        <form onSubmit={handleNext}>
          <h2 className="mb-4 text-[24px] font-semibold text-ink">Sign in</h2>
          <input
            type="text"
            name="username"
            autoComplete="username"
            autoFocus
            placeholder="Username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            className={UNDERLINE_INPUT}
          />
          {error && (
            <div className="mt-[6px] text-[12px] text-err-text">{error}</div>
          )}
          <p className="mt-4 text-[13px] text-ink-2">
            Single-admin portal — credentials are set in the backend
            environment.
          </p>
          <div className="mt-[26px] flex justify-end">
            <button type="submit" className={`${PRIMARY_BTN} px-8`}>
              Next
            </button>
          </div>
        </form>
      ) : (
        <form onSubmit={handleSubmit}>
          <button
            type="button"
            onClick={handleBack}
            className="mb-[14px] inline-flex cursor-pointer items-center gap-[6px] text-[13px] text-ink-2 hover:text-ink"
          >
            <Mi name="chevronLeft" size={12} color="currentColor" />
            {username}
          </button>
          <h2 className="mb-4 text-[24px] font-semibold text-ink">
            Enter password
          </h2>
          <input
            type="password"
            name="password"
            autoComplete="current-password"
            autoFocus
            placeholder="Password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className={UNDERLINE_INPUT}
          />
          {error && (
            <div className="mt-[6px] text-[12px] text-err-text">{error}</div>
          )}
          <div className="mt-[26px] flex justify-end">
            <button
              type="submit"
              disabled={pending}
              className={`${PRIMARY_BTN} px-7`}
            >
              Sign in
            </button>
          </div>
        </form>
      )}
    </div>
  );
}
