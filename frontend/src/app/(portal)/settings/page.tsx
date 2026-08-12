"use client";
// Settings — self-service account area (Phase 2 local auth). Real account
// details, change-password, and active-session management; the only remaining
// placeholder is the genuinely unbuilt portal-preferences note.
import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import Link from "next/link";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { RecoveryCodes } from "@/components/security/RecoveryCodes";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Flyout } from "@/components/ui/Flyout";
import { Input } from "@/components/ui/Input";
import { Mi, Spinner } from "@/components/ui/icons";
import { ApiError } from "@/lib/api/client";
import { useChangePassword, useRevokeSession, useSessions } from "@/lib/api/authMutations";
import {
  useDisableTotp,
  useEnrollTotp,
  useRegenerateRecoveryCodes,
  useVerifyEnrollTotp,
} from "@/lib/api/security";
import type { SessionInfo } from "@/lib/api/generated/types";
import { useMe } from "@/lib/api/queries";
import { isValidPassword, PASSWORD_RULE } from "@/lib/auth/validation";
import { relativeTime } from "@/lib/format";
import { pushToast } from "@/lib/stores/toastStore";

// §1.6 section heading (16px/600) + 1px rule.
function Section({ title, caption, children }: { title: string; caption?: string; children: ReactNode }) {
  return (
    <section className="mt-8 first:mt-0">
      <h2 className="text-[16px] font-semibold text-ink">{title}</h2>
      {caption ? <p className="mt-1 text-[12px] text-ink-2">{caption}</p> : null}
      <div className="mt-2 mb-[14px] h-px bg-line" />
      {children}
    </section>
  );
}

// §1.6 form row: label flex 0 0 220px, control width 300px.
function FieldRow({ label, htmlFor, children }: { label: string; htmlFor?: string; children: ReactNode }) {
  return (
    <div className="mb-[14px] flex items-center">
      <label htmlFor={htmlFor} className="flex-none basis-[220px] text-[14px] text-ink">
        {label}
      </label>
      <div className="w-[300px]">{children}</div>
    </div>
  );
}

function changePasswordError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.status === 401) return "Current password is incorrect.";
    if (err.status === 400) return `Password must be ${PASSWORD_RULE.toLowerCase()}.`;
    if (err.status === 429) return "Too many attempts — wait a minute and try again.";
    return err.detail;
  }
  return "Unable to reach the server. Try again.";
}

function AccountSection() {
  const me = useMe();

  if (me.isPending) {
    return (
      <Section title="Account">
        <Skeleton className="h-5 w-64" />
        <Skeleton className="mt-3 h-5 w-80" />
      </Section>
    );
  }
  if (me.isError) {
    return (
      <Section title="Account">
        <CardError err={me.error} />
        <div className="mt-3">
          <Button variant="secondaryCompact" onClick={() => me.refetch()}>
            Retry
          </Button>
        </div>
      </Section>
    );
  }

  return (
    <Section title="Account" caption="Your sign-in identity for this portal.">
      <FieldRow label="Display name">
        <div className="flex h-8 items-center text-[14px] text-ink">{me.data.displayName || "—"}</div>
      </FieldRow>
      <FieldRow label="Email">
        <div className="flex h-8 items-center text-[14px] text-ink tabular-nums">{me.data.email}</div>
      </FieldRow>
      {me.data.isPlatformAdmin ? (
        <FieldRow label="Role">
          <span className="inline-flex items-center rounded-fluent border border-nav-active bg-selected px-[10px] py-1 text-[12px] font-semibold text-accent">
            Platform administrator
          </span>
        </FieldRow>
      ) : null}
    </Section>
  );
}

function ChangePasswordSection() {
  const changePassword = useChangePassword();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [error, setError] = useState("");

  const nextBad = next.length > 0 && !isValidPassword(next);
  const confirmBad = confirm.length > 0 && confirm !== next;

  function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    if (!current) {
      setError("Enter your current password.");
      return;
    }
    if (!isValidPassword(next)) {
      setError(`New password must be ${PASSWORD_RULE.toLowerCase()}.`);
      return;
    }
    if (next !== confirm) {
      setError("New passwords do not match.");
      return;
    }
    setError("");
    changePassword.mutate(
      { currentPassword: current, newPassword: next },
      {
        onSuccess: () => {
          pushToast({
            kind: "ok",
            title: "Password changed",
            desc: "Other sessions were signed out.",
          });
          setCurrent("");
          setNext("");
          setConfirm("");
        },
        onError: (err) => setError(changePasswordError(err)),
      },
    );
  }

  return (
    <Section
      title="Change password"
      caption="Changing your password signs out every other session."
    >
      <form onSubmit={handleSubmit}>
        <FieldRow label="Current password" htmlFor="current-password">
          <Input
            id="current-password"
            type="password"
            name="current-password"
            autoComplete="current-password"
            value={current}
            onChange={(e) => setCurrent(e.target.value)}
            className="w-full"
          />
        </FieldRow>
        <FieldRow label="New password" htmlFor="new-password">
          <Input
            id="new-password"
            type="password"
            name="new-password"
            autoComplete="new-password"
            value={next}
            onChange={(e) => setNext(e.target.value)}
            invalid={nextBad}
            aria-describedby="new-password-hint"
            className="w-full"
          />
        </FieldRow>
        <div className="mb-[14px] flex">
          <span className="flex-none basis-[220px]" />
          <div
            id="new-password-hint"
            className={`w-[300px] text-[12px] ${nextBad ? "text-err-text" : "text-ink-3"}`}
          >
            {PASSWORD_RULE}
          </div>
        </div>
        <FieldRow label="Confirm new password" htmlFor="confirm-password">
          <Input
            id="confirm-password"
            type="password"
            name="confirm-password"
            autoComplete="new-password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            invalid={confirmBad}
            className="w-full"
          />
        </FieldRow>
        {confirmBad ? (
          <div className="mb-[14px] flex">
            <span className="flex-none basis-[220px]" />
            <div className="w-[300px] text-[12px] text-err-text">New passwords do not match.</div>
          </div>
        ) : null}
        {error ? (
          <div className="mb-[14px] flex">
            <span className="flex-none basis-[220px]" />
            <div className="w-[300px] text-[12px] text-err-text">{error}</div>
          </div>
        ) : null}
        <div className="flex">
          <span className="flex-none basis-[220px]" />
          <Button type="submit" variant="primaryCompact" disabled={changePassword.isPending}>
            {changePassword.isPending ? "Changing…" : "Change password"}
          </Button>
        </div>
      </form>
    </Section>
  );
}

// ── Two-step verification (TOTP) ─────────────────────────────────────────────

function securityError(err: unknown, wrongPasswordHint = false): string {
  if (err instanceof ApiError) {
    if (err.status === 401) return wrongPasswordHint ? "That password is incorrect." : "That code is not valid.";
    if (err.status === 409) return "This action is no longer available — refresh and try again.";
    if (err.status === 429) return "Too many attempts — wait a minute and try again.";
    return err.detail;
  }
  return "Unable to reach the server. Try again.";
}

/**
 * Enroll flyout: kicks off enrollment on mount (QR + manual key), collects a
 * 6-digit confirmation code, then reveals the one-time recovery codes behind an
 * explicit "I've saved these" gate before closing.
 */
function TotpEnrollFlyout({ onClose }: { onClose: () => void }) {
  const enroll = useEnrollTotp();
  const verify = useVerifyEnrollTotp();
  const started = useRef(false);
  const [code, setCode] = useState("");
  const [error, setError] = useState("");
  const [copiedKey, setCopiedKey] = useState(false);

  useEffect(() => {
    if (started.current) return;
    started.current = true;
    enroll.mutate();
    // enroll is stable enough; the ref guards against React's dev double-invoke.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Terminal state: codes revealed once, dismissal gated by RecoveryCodes.
  if (verify.data) {
    return (
      <Flyout title="Save your recovery codes" onClose={() => {}}>
        <RecoveryCodes
          codes={verify.data.recoveryCodes}
          doneLabel="Done"
          onDone={() => {
            pushToast({
              kind: "ok",
              title: "Two-step verification is on",
              desc: "You'll enter a code from your app at sign-in.",
            });
            onClose();
          }}
        />
      </Flyout>
    );
  }

  function submitCode() {
    if (code.length !== 6) {
      setError("Enter the 6-digit code from your app.");
      return;
    }
    setError("");
    verify.mutate({ code }, { onError: (err) => setError(securityError(err)) });
  }

  return (
    <Flyout title="Set up two-step verification" onClose={onClose}>
      {enroll.isPending ? (
        <div className="flex items-center gap-2 text-[13px] text-ink-2">
          <Spinner size={16} />
          Preparing your authenticator secret…
        </div>
      ) : enroll.isError ? (
        <div>
          <CardError err={enroll.error} />
          <div className="mt-3">
            <Button
              variant="secondaryCompact"
              onClick={() => {
                started.current = true;
                enroll.reset();
                enroll.mutate();
              }}
            >
              Retry
            </Button>
          </div>
        </div>
      ) : enroll.data ? (
        <div>
          <ol className="mb-4 list-decimal space-y-1 pl-5 text-[13px] leading-[1.5] text-ink-2">
            <li>Scan the QR code with an authenticator app (Google Authenticator, 1Password, …).</li>
            <li>Enter the 6-digit code it shows to confirm.</li>
          </ol>

          {/* eslint-disable-next-line @next/next/no-img-element */}
          <img
            src={enroll.data.qrPngDataUri}
            alt="Two-step verification QR code"
            width={180}
            height={180}
            className="mb-3 rounded-fluent border border-line"
          />

          <div className="mb-4">
            <div className="mb-[6px] text-[12px] text-ink-2">Or enter this key manually</div>
            <div className="flex items-center gap-2">
              <code className="rounded-fluent border border-line bg-canvas px-2 py-1 font-mono text-[13px] break-all text-ink">
                {enroll.data.manualKey}
              </code>
              <Button
                variant="secondaryCompact"
                onClick={async () => {
                  try {
                    if (typeof navigator !== "undefined" && navigator.clipboard) {
                      await navigator.clipboard.writeText(enroll.data!.manualKey);
                      setCopiedKey(true);
                    }
                  } catch {
                    // best-effort — the key is visible and selectable
                  }
                }}
              >
                {copiedKey ? "Copied" : "Copy"}
              </Button>
            </div>
          </div>

          <label htmlFor="totp-confirm" className="mb-[6px] block text-[13px] text-ink">
            6-digit code
          </label>
          <Input
            id="totp-confirm"
            inputMode="numeric"
            autoComplete="one-time-code"
            maxLength={6}
            placeholder="123456"
            value={code}
            onChange={(e) => {
              setCode(e.target.value.replace(/\D/g, ""));
              setError("");
            }}
            className="w-[160px] tracking-[0.3em] tabular-nums"
            autoFocus
          />
          {error ? <p className="mt-2 text-[12px] text-err-text">{error}</p> : null}

          <div className="mt-5 flex gap-2">
            <Button variant="primary" disabled={code.length !== 6 || verify.isPending} onClick={submitCode}>
              {verify.isPending ? "Verifying…" : "Confirm"}
            </Button>
            <Button variant="secondary" onClick={onClose}>
              Cancel
            </Button>
          </div>
        </div>
      ) : null}
    </Flyout>
  );
}

/** Turn-off flyout: re-prompt the password, then disable TOTP. */
function DisableTotpFlyout({ onClose }: { onClose: () => void }) {
  const disable = useDisableTotp();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");

  function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!password) {
      setError("Enter your password to confirm.");
      return;
    }
    setError("");
    disable.mutate(
      { password },
      {
        onSuccess: () => {
          pushToast({
            kind: "ok",
            title: "Two-step verification turned off",
            desc: "Your recovery codes were deleted.",
          });
          onClose();
        },
        onError: (err) => setError(securityError(err, true)),
      },
    );
  }

  return (
    <Flyout title="Turn off two-step verification" onClose={onClose}>
      <div className="mb-4 flex gap-[10px] rounded-fluent border border-err bg-err-bg px-3 py-[10px] text-[13px] leading-[1.5]">
        <Mi name="warn" size={16} color="var(--color-err)" style={{ flexShrink: 0, marginTop: 2 }} />
        <span>Turning this off deletes your recovery codes and lets anyone with your password sign in.</span>
      </div>
      <form onSubmit={submit}>
        <label htmlFor="disable-totp-password" className="mb-[6px] block text-[13px] text-ink">
          Confirm your password
        </label>
        <Input
          id="disable-totp-password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => {
            setPassword(e.target.value);
            setError("");
          }}
          className="w-full"
          autoFocus
        />
        {error ? <p className="mt-2 text-[12px] text-err-text">{error}</p> : null}
        <div className="mt-5 flex gap-2">
          <Button variant="danger" disabled={!password || disable.isPending}>
            {disable.isPending ? "Turning off…" : "Turn off"}
          </Button>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
        </div>
      </form>
    </Flyout>
  );
}

/** Regenerate flyout: re-prompt the password, then reveal the fresh codes once. */
function RegenerateCodesFlyout({ onClose }: { onClose: () => void }) {
  const regen = useRegenerateRecoveryCodes();
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");

  if (regen.data) {
    return (
      <Flyout title="New recovery codes" onClose={() => {}}>
        <RecoveryCodes
          codes={regen.data.recoveryCodes}
          doneLabel="Done"
          onDone={() => {
            pushToast({ kind: "ok", title: "Recovery codes regenerated", desc: "Older codes no longer work." });
            onClose();
          }}
        />
      </Flyout>
    );
  }

  function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!password) {
      setError("Enter your password to confirm.");
      return;
    }
    setError("");
    regen.mutate({ password }, { onError: (err) => setError(securityError(err, true)) });
  }

  return (
    <Flyout title="Regenerate recovery codes" onClose={onClose}>
      <p className="mb-4 text-[13px] leading-[1.5] text-ink-2">
        This replaces your existing recovery codes with ten new ones. Any codes you saved before will
        stop working.
      </p>
      <form onSubmit={submit}>
        <label htmlFor="regen-password" className="mb-[6px] block text-[13px] text-ink">
          Confirm your password
        </label>
        <Input
          id="regen-password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={(e) => {
            setPassword(e.target.value);
            setError("");
          }}
          className="w-full"
          autoFocus
        />
        {error ? <p className="mt-2 text-[12px] text-err-text">{error}</p> : null}
        <div className="mt-5 flex gap-2">
          <Button variant="primary" disabled={!password || regen.isPending}>
            {regen.isPending ? "Generating…" : "Regenerate codes"}
          </Button>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
        </div>
      </form>
    </Flyout>
  );
}

type TwoStepDialog = "enroll" | "disable" | "regenerate" | null;

function TwoStepSection() {
  const me = useMe();
  const [dialog, setDialog] = useState<TwoStepDialog>(null);

  if (me.isPending) {
    return (
      <Section title="Two-step verification">
        <Skeleton className="h-5 w-72" />
      </Section>
    );
  }
  if (me.isError) {
    return (
      <Section title="Two-step verification">
        <CardError err={me.error} />
        <div className="mt-3">
          <Button variant="secondaryCompact" onClick={() => me.refetch()}>
            Retry
          </Button>
        </div>
      </Section>
    );
  }

  const enabled = me.data.totpEnabled;
  const remaining = me.data.recoveryCodesRemaining;

  return (
    <Section
      title="Two-step verification"
      caption="Protect your account with a time-based one-time code from an authenticator app."
    >
      {enabled ? (
        <>
          <FieldRow label="Status">
            <span className="inline-flex items-center gap-[6px] rounded-fluent border border-ok bg-ok-bg px-[10px] py-1 text-[12px] font-semibold text-ok">
              <Mi name="checkC" size={14} color="var(--color-ok)" />
              On
            </span>
          </FieldRow>
          <FieldRow label="Recovery codes">
            <div className="flex h-8 items-center gap-2 text-[14px] text-ink tabular-nums">
              {remaining} remaining
              {remaining <= 3 ? (
                <span className="text-[12px] text-err-text">Running low — regenerate soon.</span>
              ) : null}
            </div>
          </FieldRow>
          <div className="mt-2 flex gap-2">
            <Button variant="secondaryCompact" onClick={() => setDialog("regenerate")}>
              Regenerate recovery codes
            </Button>
            <Button variant="secondaryCompact" onClick={() => setDialog("disable")}>
              Turn off two-step
            </Button>
          </div>
        </>
      ) : (
        <>
          <FieldRow label="Status">
            <span className="inline-flex items-center rounded-fluent border border-line-input bg-card px-[10px] py-1 text-[12px] text-ink-2">
              Off
            </span>
          </FieldRow>
          <div className="mt-2">
            <Button variant="primaryCompact" onClick={() => setDialog("enroll")}>
              Set up
            </Button>
          </div>
        </>
      )}

      {dialog === "enroll" ? <TotpEnrollFlyout onClose={() => setDialog(null)} /> : null}
      {dialog === "disable" ? <DisableTotpFlyout onClose={() => setDialog(null)} /> : null}
      {dialog === "regenerate" ? <RegenerateCodesFlyout onClose={() => setDialog(null)} /> : null}
    </Section>
  );
}

function SessionRow({ session }: { session: SessionInfo }) {
  const revoke = useRevokeSession();
  return (
    <tr className="border-b border-line-row last:border-b-0">
      <td className="h-10 max-w-[320px] truncate px-4" title={session.userAgent}>
        {session.userAgent || "Unknown device"}
      </td>
      <td className="px-4 text-ink-2 tabular-nums">{session.ip || "—"}</td>
      <td className="px-4 text-ink-2 tabular-nums">{relativeTime(session.lastSeenAt)}</td>
      <td className="px-4 text-ink-2 tabular-nums">{relativeTime(session.createdAt)}</td>
      <td className="px-4 text-right">
        {session.current ? (
          <span className="inline-flex items-center rounded-fluent border border-nav-active bg-selected px-2 py-[2px] text-[11px] font-semibold text-accent">
            This session
          </span>
        ) : (
          <Button
            variant="link"
            disabled={revoke.isPending}
            onClick={() =>
              revoke.mutate(session.id, {
                onSuccess: () =>
                  pushToast({ kind: "ok", title: "Session revoked", desc: "The session was signed out." }),
                onError: (err) =>
                  pushToast({
                    kind: "err",
                    title: "Could not revoke session",
                    desc: err instanceof ApiError ? err.detail : "Request failed.",
                  }),
              })
            }
          >
            Revoke
          </Button>
        )}
      </td>
    </tr>
  );
}

function SessionsSection() {
  const sessions = useSessions();

  return (
    <Section
      title="Active sessions"
      caption="Every device currently signed in as you. Revoke any you don't recognize."
    >
      <Card>
        {sessions.isPending ? (
          <div className="space-y-2 p-4">
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
          </div>
        ) : sessions.isError ? (
          <div className="p-4">
            <CardError err={sessions.error} />
            <div className="mt-3">
              <Button variant="secondaryCompact" onClick={() => sessions.refetch()}>
                Retry
              </Button>
            </div>
          </div>
        ) : (sessions.data ?? []).length === 0 ? (
          <p className="p-6 text-[13px] text-ink-2">No active sessions.</p>
        ) : (
          <table className="w-full border-collapse text-[13px]">
            <thead>
              <tr>
                {["Device / User agent", "IP", "Last seen", "Created", ""].map((h, i) => (
                  <th
                    key={h || `col-${i}`}
                    className={`border-b border-line bg-hover px-4 py-2 font-semibold ${
                      i === 4 ? "text-right" : "text-left"
                    }`}
                  >
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {(sessions.data ?? []).map((s) => (
                <SessionRow key={s.id} session={s} />
              ))}
            </tbody>
          </table>
        )}
      </Card>
    </Section>
  );
}

export default function SettingsPage() {
  return (
    <div className="max-w-[1000px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">Settings</span>
      </nav>
      <h1 className="mb-1 text-[24px] font-semibold">Settings</h1>
      <p className="mb-6 text-[13px] text-ink-2">Manage your account, password, and sessions.</p>

      <AccountSection />
      <ChangePasswordSection />
      <TwoStepSection />
      <SessionsSection />

      <Section title="Portal preferences">
        <div className="flex items-start gap-2">
          <Mi name="info" size={16} color="var(--color-ink-3)" />
          <p className="text-[13px] leading-[1.5] text-ink-2">
            Theme, language, and default filters arrive in a later iteration. Proxmox connection and
            pricing configuration live in the backend environment — see the README.
          </p>
        </div>
      </Section>
    </div>
  );
}
