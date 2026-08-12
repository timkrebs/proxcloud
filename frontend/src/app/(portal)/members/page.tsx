"use client";
// Members (tenant admin) — the people who can reach this directory, plus the
// Phase-5 invitation surface. Owners (and platform admins) get an Invite flyout
// (email + scope + role) and a Pending-invitations table with revoke; everyone
// else is told the area is owner-managed. The members list itself is an
// Owner-only backend read, so the whole surface is gated on ownership.
import { useMemo, useState } from "react";
import Link from "next/link";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { Flyout } from "@/components/ui/Flyout";
import { Input } from "@/components/ui/Input";
import { Select } from "@/components/ui/Select";
import { Mi } from "@/components/ui/icons";
import { ApiError } from "@/lib/api/client";
import type { CreateInvitationRequest, Invitation, Member, Project } from "@/lib/api/generated/types";
import { useCreateInvitation, useInvitations, useRevokeInvitation } from "@/lib/api/security";
import { useMe } from "@/lib/api/queries";
import { useMembers, useProjects } from "@/lib/api/tenant";
import { useActiveTenantId } from "@/lib/stores/uiStore";
import { pushToast } from "@/lib/stores/toastStore";
import { isValidEmail } from "@/lib/auth/validation";
import { relativeTime } from "@/lib/format";

const ROLES = ["owner", "contributor", "reader"] as const;
const ROLE_LABEL: Record<string, string> = {
  owner: "Owner",
  contributor: "Contributor",
  reader: "Reader",
};
function roleLabel(role: string): string {
  return ROLE_LABEL[role] ?? role;
}

function errText(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === "forbidden") return "You can't grant a role higher than your own.";
    if (err.status === 409) return "An invitation for that email and scope already exists.";
    if (err.status === 404) return "That project is not part of this directory.";
    if (err.status === 400) return err.message || "Check the email and role.";
    return err.detail;
  }
  if (err instanceof Error) return err.message;
  return "Request failed.";
}

// ── Invite flyout ──────────────────────────────────────────────────────────────

/** Scope options carry the concrete scopeType+scopeId the request needs. */
type ScopeChoice = { value: string; label: string; scopeType: "tenant" | "project"; scopeId: string };

function InviteFlyout({
  tenantId,
  tenantName,
  projects,
  projectsPending,
  onClose,
}: {
  tenantId: string;
  tenantName: string;
  projects: Project[];
  projectsPending: boolean;
  onClose: () => void;
}) {
  const create = useCreateInvitation();
  const [email, setEmail] = useState("");
  const [scopeValue, setScopeValue] = useState("tenant");
  const [role, setRole] = useState<(typeof ROLES)[number]>("reader");
  const [error, setError] = useState("");

  const scopes: ScopeChoice[] = useMemo(
    () => [
      { value: "tenant", label: `Entire directory — ${tenantName}`, scopeType: "tenant", scopeId: tenantId },
      ...projects.map<ScopeChoice>((p) => ({
        value: `project:${p.id}`,
        label: `Project — ${p.name}`,
        scopeType: "project",
        scopeId: p.id,
      })),
    ],
    [tenantId, tenantName, projects],
  );

  const emailBad = email.length > 0 && !isValidEmail(email);

  function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!isValidEmail(email)) {
      setError("Enter a valid email address.");
      return;
    }
    const scope = scopes.find((s) => s.value === scopeValue) ?? scopes[0];
    setError("");
    create.mutate(
      {
        email: email.trim(),
        scopeType: scope.scopeType,
        scopeId: scope.scopeId,
        role,
      } satisfies CreateInvitationRequest,
      {
        onSuccess: () => onClose(),
        onError: (err) => setError(errText(err)),
      },
    );
  }

  return (
    <Flyout title="Invite a member" onClose={onClose}>
      <p className="mb-4 text-[13px] leading-[1.5] text-ink-2">
        Send an invitation email. The recipient accepts with a secure link — no password is shared.
        You can only grant a role at or below your own.
      </p>
      <form onSubmit={submit}>
        <label htmlFor="invite-email" className="mb-[6px] block text-[13px] text-ink">
          Email
        </label>
        <Input
          id="invite-email"
          type="email"
          autoComplete="off"
          placeholder="person@example.com"
          value={email}
          onChange={(e) => {
            setEmail(e.target.value);
            setError("");
          }}
          invalid={emailBad}
          className="mb-4 w-full"
          autoFocus
        />

        <label htmlFor="invite-scope" className="mb-[6px] block text-[13px] text-ink">
          Scope
        </label>
        <Select
          id="invite-scope"
          value={scopeValue}
          onChange={(e) => setScopeValue(e.target.value)}
          className="mb-1 w-full"
        >
          {scopes.map((s) => (
            <option key={s.value} value={s.value}>
              {s.label}
            </option>
          ))}
        </Select>
        <p className="mb-4 text-[12px] text-ink-3">
          {projectsPending ? "Loading projects…" : "Directory scope grants access to every project."}
        </p>

        <label htmlFor="invite-role" className="mb-[6px] block text-[13px] text-ink">
          Role
        </label>
        <Select
          id="invite-role"
          value={role}
          onChange={(e) => setRole(e.target.value as (typeof ROLES)[number])}
          className="mb-1 w-full"
        >
          {ROLES.map((r) => (
            <option key={r} value={r}>
              {roleLabel(r)}
            </option>
          ))}
        </Select>

        {error ? <p className="mt-3 text-[12px] text-err-text">{error}</p> : null}

        <div className="mt-5 flex gap-2">
          <Button variant="primary" disabled={email.trim() === "" || create.isPending}>
            {create.isPending ? "Sending…" : "Send invitation"}
          </Button>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
        </div>
      </form>
    </Flyout>
  );
}

// ── Tables ─────────────────────────────────────────────────────────────────────

function ScopeLabel({ member, projectsById }: { member: Member; projectsById: Record<string, string> }) {
  if (member.scopeType === "tenant") return <span className="text-ink-2">Directory</span>;
  return <span className="text-ink-2">{projectsById[member.scopeId] ?? member.scopeId}</span>;
}

function MembersTable({
  members,
  projectsById,
}: {
  members: ReturnType<typeof useMembers>;
  projectsById: Record<string, string>;
}) {
  return (
    <Card>
      {members.isPending ? (
        <div className="space-y-2 p-4">
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
        </div>
      ) : members.isError ? (
        <div className="p-4">
          <CardError err={members.error} />
          <div className="mt-3">
            <Button variant="secondaryCompact" onClick={() => members.refetch()}>
              Retry
            </Button>
          </div>
        </div>
      ) : (members.data ?? []).length === 0 ? (
        <p className="p-6 text-[13px] text-ink-2">No members yet.</p>
      ) : (
        <table className="w-full border-collapse text-[13px]">
          <thead>
            <tr>
              {["Name", "Email", "Scope", "Role"].map((h) => (
                <th key={h} className="border-b border-line bg-hover px-4 py-2 text-left font-semibold">
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {(members.data ?? []).map((m) => (
              <tr key={`${m.userId}:${m.scopeType}:${m.scopeId}`} className="border-b border-line-row last:border-b-0">
                <td className="h-10 px-4 font-semibold">{m.displayName || "—"}</td>
                <td className="px-4 text-ink-2">{m.email}</td>
                <td className="px-4">
                  <ScopeLabel member={m} projectsById={projectsById} />
                </td>
                <td className="px-4 text-ink-2">{roleLabel(m.role)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

function InvitationRow({ invite }: { invite: Invitation }) {
  const revoke = useRevokeInvitation();
  const expired = invite.status === "expired";
  return (
    <tr className="border-b border-line-row last:border-b-0">
      <td className="h-10 px-4 font-semibold">{invite.email}</td>
      <td className="px-4 text-ink-2">{roleLabel(invite.role)}</td>
      <td className="px-4 text-ink-2">{invite.scopeLabel}</td>
      <td className="px-4 text-ink-2 tabular-nums">{relativeTime(invite.expiresAt)}</td>
      <td className="px-4">
        {expired ? (
          <span className="inline-flex items-center rounded-fluent border border-line-input bg-card px-2 py-[2px] text-[11px] text-ink-2">
            Expired
          </span>
        ) : (
          <span className="inline-flex items-center rounded-fluent border border-nav-active bg-selected px-2 py-[2px] text-[11px] font-semibold text-accent">
            Pending
          </span>
        )}
      </td>
      <td className="px-4 text-right">
        <Button
          variant="link"
          disabled={revoke.isPending}
          onClick={() =>
            revoke.mutate(invite.id, {
              onSuccess: () => pushRevokeToast(),
              onError: (err) => pushRevokeError(err),
            })
          }
        >
          Revoke
        </Button>
      </td>
    </tr>
  );
}

function InvitationsTable({ invitations }: { invitations: ReturnType<typeof useInvitations> }) {
  return (
    <Card>
      {invitations.isPending ? (
        <div className="space-y-2 p-4">
          <Skeleton className="h-8" />
          <Skeleton className="h-8" />
        </div>
      ) : invitations.isError ? (
        <div className="p-4">
          <CardError err={invitations.error} />
          <div className="mt-3">
            <Button variant="secondaryCompact" onClick={() => invitations.refetch()}>
              Retry
            </Button>
          </div>
        </div>
      ) : (invitations.data ?? []).length === 0 ? (
        <p className="p-6 text-[13px] text-ink-2">No pending invitations.</p>
      ) : (
        <table className="w-full border-collapse text-[13px]">
          <thead>
            <tr>
              {["Email", "Role", "Scope", "Expires", "Status", ""].map((h, i) => (
                <th
                  key={h || `col-${i}`}
                  className={`border-b border-line bg-hover px-4 py-2 font-semibold ${
                    i === 5 ? "text-right" : "text-left"
                  }`}
                >
                  {h}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {(invitations.data ?? []).map((inv) => (
              <InvitationRow key={inv.id} invite={inv} />
            ))}
          </tbody>
        </table>
      )}
    </Card>
  );
}

// ── Toast helpers (kept out of the row so it stays presentational) ──────────────
function pushRevokeToast() {
  pushToast({ kind: "ok", title: "Invitation revoked", desc: "The invitation link no longer works." });
}
function pushRevokeError(err: unknown) {
  pushToast({ kind: "err", title: "Could not revoke invitation", desc: errText(err) });
}

// ── Page ────────────────────────────────────────────────────────────────────────

export default function MembersPage() {
  const me = useMe();
  const activeTenantId = useActiveTenantId();
  const isAdmin = !!me.data?.isPlatformAdmin;
  const activeTenant = me.data?.tenants.find((t) => t.id === activeTenantId);
  const tenantRole = activeTenant?.role;
  const canManage = isAdmin || tenantRole === "owner";
  // members + invitations are Owner-gated backend routes; only query them when
  // the caller can manage, so non-Owners don't generate guaranteed 403s.
  const members = useMembers(canManage);
  const invitations = useInvitations(canManage);
  const projects = useProjects();
  const [inviting, setInviting] = useState(false);

  const projectsById = useMemo(() => {
    const m: Record<string, string> = {};
    for (const p of projects.data ?? []) m[p.id] = p.name;
    return m;
  }, [projects.data]);

  return (
    <div className="max-w-[1100px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">Members</span>
      </nav>
      <h1 className="mb-1 text-[24px] font-semibold">Members</h1>
      <p className="mb-4 text-[12px] text-ink-2">
        People with access to this directory, and the invitations you&apos;ve sent.
      </p>

      {me.isPending ? (
        <div className="space-y-2">
          <Skeleton className="h-8 w-full" />
          <Skeleton className="h-8 w-full" />
        </div>
      ) : !canManage ? (
        <EmptyState
          icon="person"
          title="Owners manage members"
          body="Only directory owners can view members and send invitations. Ask an owner if you need access."
          variant="page"
        />
      ) : (
        <>
          <div className="mb-3 flex items-center border-b border-line">
            <button
              type="button"
              onClick={() => setInviting(true)}
              className="flex h-9 cursor-pointer items-center gap-[6px] border-none bg-transparent px-[10px] text-[13px] text-ink hover:bg-hover"
            >
              <Mi name="plus" size={14} color="var(--color-accent)" />
              Invite member
            </button>
            <button
              type="button"
              onClick={() => {
                members.refetch();
                invitations.refetch();
              }}
              className="flex h-9 cursor-pointer items-center gap-[6px] border-none bg-transparent px-[10px] text-[13px] text-ink hover:bg-hover"
            >
              <Mi name="restart" size={14} />
              Refresh
            </button>
          </div>

          <MembersTable members={members} projectsById={projectsById} />

          <h2 className="mt-8 mb-2 text-[16px] font-semibold text-ink">Pending invitations</h2>
          <InvitationsTable invitations={invitations} />

          {inviting && activeTenantId ? (
            <InviteFlyout
              tenantId={activeTenantId}
              tenantName={activeTenant?.name ?? "this directory"}
              projects={projects.data ?? []}
              projectsPending={projects.isPending}
              onClose={() => setInviting(false)}
            />
          ) : null}
        </>
      )}
    </div>
  );
}
