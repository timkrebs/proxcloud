"use client";
// Two-step sign-in card — design-inventory §3.10, Phase 2 local auth:
// step 1 collects the email, step 2 the password; submit posts {email,password}
// to POST /api/auth/login. Errors are deliberately generic (no user
// enumeration). SSO is a disabled placeholder (OIDC is deferred).

import { useState } from "react";
import { useRouter } from "next/navigation";
import { BrandLogo, Mi } from "@/components/ui/icons";
import { apiFetch, ApiError } from "@/lib/api/client";
import { isValidEmail } from "@/lib/auth/validation";

// §3.10 underline input: h 34, border-bottom #8A8886 → accent on focus, 15px
const UNDERLINE_INPUT =
  "h-[34px] w-full border-0 border-b border-line-input bg-transparent px-[2px] text-[15px] text-ink outline-none placeholder:text-ink-3 focus:border-b-accent";

// §4.1 primary button (32px, 14px/600, radius 2); px set per step below
const PRIMARY_BTN =
  "inline-flex h-8 cursor-pointer items-center justify-center whitespace-nowrap rounded-fluent border border-transparent bg-accent text-[14px] font-semibold text-white select-none enabled:hover:bg-accent-hover enabled:active:bg-accent-active disabled:cursor-default disabled:opacity-70";

/** Map a login failure to one safe, non-enumerating line. */
function loginErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401) return "Incorrect email or password.";
    if (err.status === 429) return "Too many attempts — wait a minute and try again.";
    return err.detail;
  }
  return "Unable to reach the server. Try again.";
}

export function SignInCard() {
  const router = useRouter();
  const [step, setStep] = useState<"email" | "password">("email");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);

  function handleNext(e: React.FormEvent) {
    e.preventDefault();
    if (!email.trim()) {
      setError("Enter your email.");
      return;
    }
    if (!isValidEmail(email)) {
      setError("Enter a valid email address.");
      return;
    }
    setError("");
    setStep("password");
  }

  function handleBack() {
    setStep("email");
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
        body: JSON.stringify({ email: email.trim(), password }),
      });
      router.push("/dashboard");
    } catch (err) {
      setError(loginErrorMessage(err));
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

      {step === "email" ? (
        <form onSubmit={handleNext}>
          <h2 className="mb-4 text-[24px] font-semibold text-ink">Sign in</h2>
          <label htmlFor="signin-email" className="sr-only">
            Email
          </label>
          <input
            id="signin-email"
            type="email"
            name="email"
            autoComplete="username"
            autoFocus
            placeholder="Email"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            className={UNDERLINE_INPUT}
          />
          {error && (
            <div className="mt-[6px] text-[12px] text-err-text">{error}</div>
          )}
          <div className="mt-[26px] flex justify-end">
            <button type="submit" className={`${PRIMARY_BTN} px-8`}>
              Next
            </button>
          </div>

          {/* SSO placeholder — deferred (OIDC arrives later); disabled + tooltip */}
          <div className="mt-6 flex items-center gap-3 text-[12px] text-ink-3">
            <span className="h-px flex-1 bg-line" />
            or
            <span className="h-px flex-1 bg-line" />
          </div>
          <button
            type="button"
            disabled
            title="SSO arrives in a later release"
            className="mt-4 flex h-11 w-full cursor-not-allowed items-center justify-center gap-2 rounded-fluent border border-line-input bg-card text-[14px] text-ink-3 shadow-auth"
          >
            <Mi name="globe" size={16} color="currentColor" />
            Sign in with SSO
          </button>
        </form>
      ) : (
        <form onSubmit={handleSubmit}>
          <button
            type="button"
            onClick={handleBack}
            className="mb-[14px] inline-flex cursor-pointer items-center gap-[6px] text-[13px] text-ink-2 hover:text-ink"
          >
            <Mi name="chevronLeft" size={12} color="currentColor" />
            {email}
          </button>
          <h2 className="mb-4 text-[24px] font-semibold text-ink">
            Enter password
          </h2>
          <label htmlFor="signin-password" className="sr-only">
            Password
          </label>
          <input
            id="signin-password"
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
