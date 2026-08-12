"use client";
// First-run bootstrap card — creates the platform administrator when the
// backend reports needsBootstrap. Posts POST /api/auth/bootstrap; on 204 the
// server sets the session cookie and we go straight to the dashboard.
// Shares the sign-in card's design language (brand logo, 24px heading,
// underline inputs).

import { useState } from "react";
import { useRouter } from "next/navigation";
import { BrandLogo } from "@/components/ui/icons";
import { apiFetch, ApiError } from "@/lib/api/client";
import { isValidEmail, isValidPassword, PASSWORD_RULE } from "@/lib/auth/validation";

const UNDERLINE_INPUT =
  "h-[34px] w-full border-0 border-b bg-transparent px-[2px] text-[15px] text-ink outline-none placeholder:text-ink-3";

const PRIMARY_BTN =
  "inline-flex h-8 cursor-pointer items-center justify-center whitespace-nowrap rounded-fluent border border-transparent bg-accent px-7 text-[14px] font-semibold text-white select-none enabled:hover:bg-accent-hover enabled:active:bg-accent-active disabled:cursor-default disabled:opacity-70";

/** Map a bootstrap failure to a user-facing line (excluding 409, handled by caller). */
function bootstrapErrorMessage(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 400) return `Password must be ${PASSWORD_RULE.toLowerCase()}.`;
    if (err.status === 429) return "Too many attempts — wait a minute and try again.";
    return err.detail;
  }
  return "Unable to reach the server. Try again.";
}

export function BootstrapCard({ onAlreadyBootstrapped }: { onAlreadyBootstrapped: () => void }) {
  const router = useRouter();
  const [displayName, setDisplayName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");
  const [pending, setPending] = useState(false);

  // Inline field feedback (only after the user has typed something).
  const emailBad = email.length > 0 && !isValidEmail(email);
  const passwordBad = password.length > 0 && !isValidPassword(password);
  const confirmBad = confirm.length > 0 && confirm !== password;

  function borderClass(bad: boolean): string {
    return bad ? "border-err" : "border-line-input focus:border-b-accent";
  }

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!displayName.trim()) {
      setError("Enter a display name.");
      return;
    }
    if (!isValidEmail(email)) {
      setError("Enter a valid email address.");
      return;
    }
    if (!isValidPassword(password)) {
      setError(`Password must be ${PASSWORD_RULE.toLowerCase()}.`);
      return;
    }
    if (password !== confirm) {
      setError("Passwords do not match.");
      return;
    }
    setError("");
    setPending(true);
    try {
      await apiFetch<void>("/api/auth/bootstrap", {
        method: "POST",
        body: JSON.stringify({ email: email.trim(), password, displayName: displayName.trim() }),
      });
      router.push("/dashboard");
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        // Someone else bootstrapped first — fall back to the sign-in card.
        onAlreadyBootstrapped();
        return;
      }
      setError(bootstrapErrorMessage(err));
      setPending(false);
    }
  }

  return (
    <div className="w-[400px] max-w-full rounded-fluent border border-line bg-card p-10 shadow-auth">
      <div className="mb-[22px] flex items-center gap-[9px]">
        <BrandLogo size={24} />
        <span className="text-[16px] font-semibold text-ink">Proxcloud</span>
      </div>

      <h2 className="text-[24px] font-semibold text-ink">Welcome to Proxcloud</h2>
      <p className="mt-1 mb-5 text-[13px] leading-[1.5] text-ink-2">
        Create the platform administrator. This is the first and only account
        until you invite others.
      </p>

      <form onSubmit={handleSubmit} className="flex flex-col gap-4">
        <div>
          <label htmlFor="bootstrap-name" className="mb-1 block text-[12px] text-ink-2">
            Display name
          </label>
          <input
            id="bootstrap-name"
            type="text"
            name="name"
            autoComplete="name"
            autoFocus
            placeholder="Ada Lovelace"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            className={`${UNDERLINE_INPUT} border-line-input focus:border-b-accent`}
          />
        </div>

        <div>
          <label htmlFor="bootstrap-email" className="mb-1 block text-[12px] text-ink-2">
            Email
          </label>
          <input
            id="bootstrap-email"
            type="email"
            name="email"
            autoComplete="username"
            placeholder="admin@example.com"
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            aria-invalid={emailBad}
            className={`${UNDERLINE_INPUT} ${borderClass(emailBad)}`}
          />
          {emailBad && (
            <div className="mt-[4px] text-[12px] text-err-text">Enter a valid email address.</div>
          )}
        </div>

        <div>
          <label htmlFor="bootstrap-password" className="mb-1 block text-[12px] text-ink-2">
            Password
          </label>
          <input
            id="bootstrap-password"
            type="password"
            name="new-password"
            autoComplete="new-password"
            placeholder="Password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            aria-invalid={passwordBad}
            aria-describedby="bootstrap-password-hint"
            className={`${UNDERLINE_INPUT} ${borderClass(passwordBad)}`}
          />
          <div
            id="bootstrap-password-hint"
            className={`mt-[4px] text-[12px] ${passwordBad ? "text-err-text" : "text-ink-3"}`}
          >
            {PASSWORD_RULE}
          </div>
        </div>

        <div>
          <label htmlFor="bootstrap-confirm" className="mb-1 block text-[12px] text-ink-2">
            Confirm password
          </label>
          <input
            id="bootstrap-confirm"
            type="password"
            name="confirm-password"
            autoComplete="new-password"
            placeholder="Re-enter password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            aria-invalid={confirmBad}
            className={`${UNDERLINE_INPUT} ${borderClass(confirmBad)}`}
          />
          {confirmBad && (
            <div className="mt-[4px] text-[12px] text-err-text">Passwords do not match.</div>
          )}
        </div>

        {error && <div className="text-[12px] text-err-text">{error}</div>}

        <div className="mt-2 flex justify-end">
          <button type="submit" disabled={pending} className={PRIMARY_BTN}>
            {pending ? "Creating…" : "Create administrator"}
          </button>
        </div>
      </form>
    </div>
  );
}
