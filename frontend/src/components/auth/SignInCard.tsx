"use client";
// Two-step sign-in card — design-inventory §3.10, Phase 2 local auth + the
// Phase-5 second factor: step 1 collects the email, step 2 the password. Login
// now returns 200 LoginResponse{totpRequired}. When the account has TOTP on, no
// session is issued yet — a proxcloud_totp challenge cookie is set and the card
// advances to a third step that posts a 6-digit code (or a recovery code) to
// POST /api/auth/login/totp. Errors stay deliberately generic (no enumeration).
// SSO is a disabled placeholder (OIDC is deferred).

import { useState } from "react";
import { useRouter } from "next/navigation";
import { BrandLogo, Mi } from "@/components/ui/icons";
import { apiFetch, ApiError } from "@/lib/api/client";
import type { LoginResponse, LoginTOTPRequest } from "@/lib/api/generated/types";
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

/**
 * Where to land after a completed sign-in. Honors a same-origin ?returnTo=
 * relative path (used by the invite-accept "Sign in to accept" link) and
 * otherwise defaults to the dashboard. Rejects absolute/protocol-relative URLs
 * so a crafted link can never bounce the user off-site.
 */
export function resolveReturnTo(raw: string | null): string {
  if (raw && raw.startsWith("/") && !raw.startsWith("//")) return raw;
  return "/dashboard";
}

function returnToFromLocation(): string {
  if (typeof window === "undefined") return "/dashboard";
  return resolveReturnTo(new URLSearchParams(window.location.search).get("returnTo"));
}

export function SignInCard() {
  const router = useRouter();
  const [step, setStep] = useState<"email" | "password" | "totp">("email");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [code, setCode] = useState("");
  const [useRecovery, setUseRecovery] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
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

  /** Return to the email step, resetting the second-factor state. */
  function restartFromChallenge(message: string) {
    setStep("email");
    setPassword("");
    setCode("");
    setUseRecovery(false);
    setError("");
    setNotice(message);
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!password) {
      setError("Enter your password.");
      return;
    }
    setError("");
    setNotice("");
    setPending(true);
    try {
      const res = await apiFetch<LoginResponse>("/api/auth/login", {
        method: "POST",
        body: JSON.stringify({ email: email.trim(), password }),
      });
      if (res.totpRequired) {
        // No session yet — a challenge cookie is set; collect the second factor.
        setPending(false);
        setCode("");
        setUseRecovery(false);
        setStep("totp");
        return;
      }
      router.push(returnToFromLocation());
    } catch (err) {
      setError(loginErrorMessage(err));
      setPending(false);
    }
  }

  async function handleTotpSubmit(e: React.FormEvent) {
    e.preventDefault();
    const trimmed = code.trim();
    if (!trimmed) {
      setError(useRecovery ? "Enter a recovery code." : "Enter the 6-digit code.");
      return;
    }
    setError("");
    setPending(true);
    try {
      await apiFetch<void>("/api/auth/login/totp", {
        method: "POST",
        body: JSON.stringify({ code: trimmed } satisfies LoginTOTPRequest),
      });
      router.push(returnToFromLocation());
    } catch (err) {
      setPending(false);
      if (err instanceof ApiError && err.code === "totp_challenge_expired") {
        // The interim challenge lapsed (timeout, single-use, or lockout) — the
        // (auth) group has no toast host, so surface the notice on the restart.
        restartFromChallenge("Your sign-in timed out. Enter your password again.");
        return;
      }
      if (err instanceof ApiError && err.status === 401) {
        setError(
          useRecovery ? "That recovery code is not valid." : "That code is not valid. Try again.",
        );
        return;
      }
      setError(loginErrorMessage(err));
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
          {notice && (
            <div className="mt-[6px] text-[12px] text-ink-2">{notice}</div>
          )}
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
      ) : step === "password" ? (
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
      ) : (
        <form onSubmit={handleTotpSubmit}>
          <button
            type="button"
            onClick={() => restartFromChallenge("")}
            className="mb-[14px] inline-flex cursor-pointer items-center gap-[6px] text-[13px] text-ink-2 hover:text-ink"
          >
            <Mi name="chevronLeft" size={12} color="currentColor" />
            {email}
          </button>
          <h2 className="mb-1 text-[24px] font-semibold text-ink">
            Two-step verification
          </h2>
          <p className="mb-4 text-[13px] leading-[1.5] text-ink-2">
            {useRecovery
              ? "Enter one of your saved recovery codes."
              : "Enter the 6-digit code from your authenticator app."}
          </p>
          <label htmlFor="signin-totp" className="sr-only">
            {useRecovery ? "Recovery code" : "Verification code"}
          </label>
          <input
            id="signin-totp"
            type="text"
            name="one-time-code"
            autoComplete="one-time-code"
            autoFocus
            inputMode={useRecovery ? "text" : "numeric"}
            maxLength={useRecovery ? 11 : 6}
            placeholder={useRecovery ? "XXXXX-XXXXX" : "123456"}
            value={code}
            onChange={(e) => {
              const raw = e.target.value;
              // TOTP mode accepts digits only; recovery mode is unconstrained.
              setCode(useRecovery ? raw : raw.replace(/\D/g, ""));
            }}
            className={`${UNDERLINE_INPUT} tabular-nums tracking-[0.2em]`}
          />
          {error && (
            <div className="mt-[6px] text-[12px] text-err-text">{error}</div>
          )}
          <div className="mt-[26px] flex items-center justify-between">
            <button
              type="button"
              onClick={() => {
                setUseRecovery((v) => !v);
                setCode("");
                setError("");
              }}
              className="cursor-pointer text-[12px] text-accent hover:text-accent-active"
            >
              {useRecovery ? "Use your authenticator app instead" : "Use a recovery code instead"}
            </button>
            <button
              type="submit"
              disabled={pending}
              className={`${PRIMARY_BTN} px-7`}
            >
              Verify
            </button>
          </div>
        </form>
      )}
    </div>
  );
}
