"use client";
// Public invitation-accept screen (Phase 5). Lives in the (auth) route group,
// which has no React Query provider and no toast host — so it uses plain
// apiFetch and inline messaging. It fetches the enumeration-safe
// InvitationDetails (a bad/expired/used token returns a generic 404), then
// branches three ways:
//   • requiresAccount        → collect displayName + password (new account)
//   • signedInMatches        → one-click attach ("Accept invitation")
//   • neither                → an account exists but the caller isn't signed in
//                              as it → "Sign in to accept"
// A successful accept (204) sets the session + active tenant server-side; we
// clear the client's persisted tenant so PortalChrome adopts the invited one,
// then land on the dashboard.
import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { useParams, useRouter } from "next/navigation";

import { BrandLogo, Mi, Spinner } from "@/components/ui/icons";
import { apiFetch, ApiError } from "@/lib/api/client";
import type { AcceptInvitationRequest, InvitationDetails } from "@/lib/api/generated/types";
import { isValidPassword, PASSWORD_RULE } from "@/lib/auth/validation";
import { useUiStore } from "@/lib/stores/uiStore";
import { formatDateTime } from "@/lib/format";

const CARD = "w-[420px] max-w-full rounded-fluent border border-line bg-card p-10 shadow-auth";

const PRIMARY_BTN =
  "inline-flex h-8 cursor-pointer items-center justify-center whitespace-nowrap rounded-fluent border border-transparent bg-accent px-5 text-[14px] font-semibold text-white select-none enabled:hover:bg-accent-hover enabled:active:bg-accent-active disabled:cursor-default disabled:opacity-70";

const ROLE_LABEL: Record<string, string> = {
  owner: "Owner",
  contributor: "Contributor",
  reader: "Reader",
};

function roleLabel(role: string): string {
  return ROLE_LABEL[role] ?? role;
}

function BrandRow() {
  return (
    <div className="mb-[22px] flex items-center gap-[9px]">
      <BrandLogo size={24} />
      <span className="text-[16px] font-semibold text-ink">Proxcloud</span>
    </div>
  );
}

/** The tenant/role/scope facts, shown above every accept action. */
function InviteFacts({ details }: { details: InvitationDetails }) {
  return (
    <div className="mb-5 rounded-fluent border border-line bg-canvas px-4 py-3 text-[13px]">
      <div className="flex justify-between gap-3 py-[3px]">
        <span className="text-ink-2">Directory</span>
        <span className="font-semibold text-ink">{details.tenantName}</span>
      </div>
      <div className="flex justify-between gap-3 py-[3px]">
        <span className="text-ink-2">Scope</span>
        <span className="text-ink">{details.scopeLabel}</span>
      </div>
      <div className="flex justify-between gap-3 py-[3px]">
        <span className="text-ink-2">Role</span>
        <span className="text-ink">{roleLabel(details.role)}</span>
      </div>
      <div className="flex justify-between gap-3 py-[3px]">
        <span className="text-ink-2">Invited</span>
        <span className="text-ink tabular-nums">{details.email}</span>
      </div>
    </div>
  );
}

/** Post-accept navigation: adopt the invited tenant, then go to the dashboard. */
function useAcceptSuccess() {
  const router = useRouter();
  return useCallback(() => {
    // The accept response already set the session's active tenant to the invited
    // tenant. Clearing the persisted client id makes PortalChrome fall back to
    // the session's active tenant (the invited one) rather than a stale choice.
    useUiStore.getState().setActiveTenant(null);
    router.push("/dashboard");
  }, [router]);
}

type AcceptErr = { message: string; action?: "signin" | "signout" };

/** Map an accept failure to a message plus an optional recovery action. */
function acceptError(err: unknown): AcceptErr {
  if (err instanceof ApiError) {
    if (err.code === "email_mismatch") {
      return {
        message: "You're signed in as a different account. Sign out to accept this invitation.",
        action: "signout",
      };
    }
    if (err.code === "account_exists") {
      return {
        message: "An account already exists for this email. Sign in to accept.",
        action: "signin",
      };
    }
    if (err.status === 409) return { message: "This invitation was just used or revoked." };
    if (err.status === 404) return { message: "This invitation is no longer valid." };
    if (err.status === 400) return { message: err.message || "Check the details and try again." };
    if (err.status === 429) return { message: "Too many attempts — wait a minute and try again." };
    return { message: err.detail };
  }
  return { message: "Unable to reach the server. Try again." };
}

// ── States ────────────────────────────────────────────────────────────────────

function Shell({ children }: { children: React.ReactNode }) {
  return (
    <main className="flex min-h-screen flex-col items-center justify-center bg-canvas px-5 py-10">
      <div className={CARD}>{children}</div>
      <Link href="/signin" className="mt-[18px] text-[12px] text-ink-2 hover:text-ink">
        ← Go to sign in
      </Link>
    </main>
  );
}

function LoadingCard() {
  return (
    <Shell>
      <BrandRow />
      <div className="flex items-center gap-2 text-[13px] text-ink-2">
        <Spinner size={16} />
        Loading invitation…
      </div>
      <div className="mt-5 space-y-2" aria-hidden>
        <div className="h-5 w-56 animate-pulse rounded-fluent bg-hover" />
        <div className="h-20 w-full animate-pulse rounded-fluent bg-hover" />
        <div className="h-8 w-32 animate-pulse rounded-fluent bg-hover" />
      </div>
    </Shell>
  );
}

function InvalidCard() {
  return (
    <Shell>
      <BrandRow />
      <div className="mb-3 flex justify-center">
        <Mi name="warn" size={40} color="var(--color-line-soft)" strokeWidth={1} />
      </div>
      <h2 className="text-center text-[18px] font-semibold text-ink">Invitation unavailable</h2>
      <p className="mx-auto mt-2 max-w-[320px] text-center text-[13px] leading-[1.5] text-ink-2">
        This invitation link is invalid, has expired, or has already been used. Ask an owner of the
        directory to send a new one.
      </p>
      <div className="mt-6 flex justify-center">
        <Link href="/signin" className={PRIMARY_BTN}>
          Go to sign in
        </Link>
      </div>
    </Shell>
  );
}

function LoadErrorCard({ onRetry }: { onRetry: () => void }) {
  return (
    <Shell>
      <BrandRow />
      <div className="mb-3 flex items-start gap-2">
        <Mi name="warn" size={16} color="var(--color-err)" />
        <span className="text-[13px] leading-[1.4] text-ink-2">
          Couldn&apos;t load this invitation. Check your connection and try again.
        </span>
      </div>
      <button type="button" onClick={onRetry} className={PRIMARY_BTN}>
        Retry
      </button>
    </Shell>
  );
}

// ── Accept forms ────────────────────────────────────────────────────────────

function ErrorLine({ err, token }: { err: AcceptErr; token: string }) {
  return (
    <div className="mt-3 text-[12px] text-err-text">
      {err.message}
      {err.action === "signin" ? (
        <>
          {" "}
          <Link
            href={`/signin?returnTo=${encodeURIComponent(`/invite/${token}`)}`}
            className="text-accent hover:text-accent-active"
          >
            Sign in
          </Link>
        </>
      ) : null}
    </div>
  );
}

/** New-account branch: collect displayName + password, then accept. */
function NewAccountForm({ token, details }: { token: string; details: InvitationDetails }) {
  const onSuccess = useAcceptSuccess();
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [err, setErr] = useState<AcceptErr | null>(null);
  const [pending, setPending] = useState(false);

  const passwordBad = password.length > 0 && !isValidPassword(password);
  const confirmBad = confirm.length > 0 && confirm !== password;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!displayName.trim()) {
      setErr({ message: "Enter your name." });
      return;
    }
    if (!isValidPassword(password)) {
      setErr({ message: `Password must be ${PASSWORD_RULE.toLowerCase()}.` });
      return;
    }
    if (password !== confirm) {
      setErr({ message: "Passwords do not match." });
      return;
    }
    setErr(null);
    setPending(true);
    try {
      await apiFetch<void>(`/api/auth/invitations/${encodeURIComponent(token)}/accept`, {
        method: "POST",
        body: JSON.stringify({
          displayName: displayName.trim(),
          password,
        } satisfies AcceptInvitationRequest),
      });
      onSuccess();
    } catch (e2) {
      setErr(acceptError(e2));
      setPending(false);
    }
  }

  return (
    <form onSubmit={submit}>
      <BrandRow />
      <h2 className="mb-1 text-[24px] font-semibold text-ink">Create your account</h2>
      <p className="mb-5 text-[13px] leading-[1.5] text-ink-2">
        You&apos;ve been invited to <strong>{details.tenantName}</strong>. Set a name and password to
        finish creating your account.
      </p>
      <InviteFacts details={details} />

      <label htmlFor="invite-name" className="mb-[6px] block text-[13px] text-ink">
        Full name
      </label>
      <input
        id="invite-name"
        type="text"
        autoComplete="name"
        autoFocus
        value={displayName}
        onChange={(e) => setDisplayName(e.target.value)}
        className="mb-4 h-8 w-full rounded-fluent border border-line-input bg-card px-2 text-[14px] outline-none focus:border-accent"
      />

      <label htmlFor="invite-password" className="mb-[6px] block text-[13px] text-ink">
        Password
      </label>
      <input
        id="invite-password"
        type="password"
        autoComplete="new-password"
        value={password}
        onChange={(e) => setPassword(e.target.value)}
        aria-describedby="invite-password-hint"
        className={`mb-1 h-8 w-full rounded-fluent border bg-card px-2 text-[14px] outline-none ${
          passwordBad ? "border-err" : "border-line-input focus:border-accent"
        }`}
      />
      <p
        id="invite-password-hint"
        className={`mb-4 text-[12px] ${passwordBad ? "text-err-text" : "text-ink-3"}`}
      >
        {PASSWORD_RULE}
      </p>

      <label htmlFor="invite-confirm" className="mb-[6px] block text-[13px] text-ink">
        Confirm password
      </label>
      <input
        id="invite-confirm"
        type="password"
        autoComplete="new-password"
        value={confirm}
        onChange={(e) => setConfirm(e.target.value)}
        className={`h-8 w-full rounded-fluent border bg-card px-2 text-[14px] outline-none ${
          confirmBad ? "border-err" : "border-line-input focus:border-accent"
        }`}
      />
      {confirmBad ? <p className="mt-1 text-[12px] text-err-text">Passwords do not match.</p> : null}

      {err ? <ErrorLine err={err} token={token} /> : null}

      <div className="mt-6 flex justify-end">
        <button type="submit" disabled={pending} className={PRIMARY_BTN}>
          {pending ? "Creating…" : "Create account & accept"}
        </button>
      </div>
    </form>
  );
}

/** Attach branch: caller is already signed in as the invited email. */
function AttachCard({ token, details }: { token: string; details: InvitationDetails }) {
  const onSuccess = useAcceptSuccess();
  const [err, setErr] = useState<AcceptErr | null>(null);
  const [pending, setPending] = useState(false);

  async function accept() {
    setErr(null);
    setPending(true);
    try {
      await apiFetch<void>(`/api/auth/invitations/${encodeURIComponent(token)}/accept`, {
        method: "POST",
        body: JSON.stringify({ displayName: "", password: "" } satisfies AcceptInvitationRequest),
      });
      onSuccess();
    } catch (e2) {
      setErr(acceptError(e2));
      setPending(false);
    }
  }

  async function signOut() {
    try {
      await apiFetch<void>("/api/auth/logout", { method: "POST", body: "{}" });
    } finally {
      // Reload the invite as a signed-out visitor so it re-branches correctly.
      window.location.reload();
    }
  }

  return (
    <div>
      <BrandRow />
      <h2 className="mb-1 text-[24px] font-semibold text-ink">Accept invitation</h2>
      <p className="mb-5 text-[13px] leading-[1.5] text-ink-2">
        Signed in as <strong>{details.email}</strong>. Accept to join{" "}
        <strong>{details.tenantName}</strong>.
      </p>
      <InviteFacts details={details} />

      {err ? <ErrorLine err={err} token={token} /> : null}
      {err?.action === "signout" ? (
        <button
          type="button"
          onClick={signOut}
          className="mt-2 text-[12px] text-accent hover:text-accent-active"
        >
          Sign out and try again
        </button>
      ) : null}

      <div className="mt-6 flex justify-end">
        <button type="button" onClick={accept} disabled={pending} className={PRIMARY_BTN}>
          {pending ? "Accepting…" : "Accept invitation"}
        </button>
      </div>
    </div>
  );
}

/** Existing-account, wrong (or no) session: point the caller at sign-in. */
function SignInToAcceptCard({ token, details }: { token: string; details: InvitationDetails }) {
  return (
    <div>
      <BrandRow />
      <h2 className="mb-1 text-[24px] font-semibold text-ink">You&apos;re invited</h2>
      <p className="mb-5 text-[13px] leading-[1.5] text-ink-2">
        An account already exists for <strong>{details.email}</strong>. Sign in to accept your
        invitation to <strong>{details.tenantName}</strong>.
      </p>
      <InviteFacts details={details} />
      <div className="mt-6 flex justify-end">
        <Link
          href={`/signin?returnTo=${encodeURIComponent(`/invite/${token}`)}`}
          className={PRIMARY_BTN}
        >
          Sign in to accept
        </Link>
      </div>
    </div>
  );
}

// ── Page ──────────────────────────────────────────────────────────────────────

type Status =
  | { kind: "loading" }
  | { kind: "loaded"; details: InvitationDetails }
  | { kind: "invalid" }
  | { kind: "error" };

export default function InviteAcceptPage() {
  const params = useParams<{ token: string }>();
  const token = params.token;
  const [status, setStatus] = useState<Status>({ kind: "loading" });

  const load = useCallback(() => {
    let cancelled = false;
    setStatus({ kind: "loading" });
    apiFetch<InvitationDetails>(`/api/auth/invitations/${encodeURIComponent(token)}`)
      .then((details) => {
        if (!cancelled) setStatus({ kind: "loaded", details });
      })
      .catch((err) => {
        if (cancelled) return;
        // Enumeration-safe: an unknown/expired/used token is a generic 404.
        setStatus(err instanceof ApiError && err.status === 404 ? { kind: "invalid" } : { kind: "error" });
      });
    return () => {
      cancelled = true;
    };
  }, [token]);

  useEffect(() => load(), [load]);

  if (status.kind === "loading") return <LoadingCard />;
  if (status.kind === "invalid") return <InvalidCard />;
  if (status.kind === "error") return <LoadErrorCard onRetry={load} />;

  const { details } = status;
  const expiresLabel = formatDateTime(details.expiresAt);

  let body: React.ReactNode;
  if (details.requiresAccount) {
    body = <NewAccountForm token={token} details={details} />;
  } else if (details.signedInMatches) {
    body = <AttachCard token={token} details={details} />;
  } else {
    body = <SignInToAcceptCard token={token} details={details} />;
  }

  return (
    <main className="flex min-h-screen flex-col items-center justify-center bg-canvas px-5 py-10">
      <div className={CARD}>{body}</div>
      <p className="mt-[14px] text-[12px] text-ink-3">Invitation expires {expiresLabel}</p>
      <Link href="/signin" className="mt-[8px] text-[12px] text-ink-2 hover:text-ink">
        ← Go to sign in
      </Link>
    </main>
  );
}
