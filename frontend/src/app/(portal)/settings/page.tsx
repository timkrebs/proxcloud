"use client";
// Settings — self-service account area (Phase 2 local auth). Real account
// details, change-password, and active-session management; the only remaining
// placeholder is the genuinely unbuilt portal-preferences note.
import { useState } from "react";
import type { ReactNode } from "react";
import Link from "next/link";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { Input } from "@/components/ui/Input";
import { Mi } from "@/components/ui/icons";
import { ApiError } from "@/lib/api/client";
import { useChangePassword, useRevokeSession, useSessions } from "@/lib/api/authMutations";
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
