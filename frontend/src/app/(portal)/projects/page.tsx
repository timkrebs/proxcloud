"use client";
// Projects (resource groups) — the tenant's scoping units, backed by Proxmox
// pools. Reader sees the list; Owner gets create / rename / delete. Delete is a
// typed-name confirmation and is only allowed when the project is empty (the
// backend re-checks and returns 409 otherwise).
import { Fragment, useMemo, useState } from "react";
import Link from "next/link";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { QuotaBars, QuotaBarsCard, allUnlimited } from "@/components/quota/QuotaBars";
import { QuotaEditorFlyout } from "@/components/quota/QuotaEditorFlyout";
import { Button } from "@/components/ui/Button";
import { Card } from "@/components/ui/Card";
import { EmptyState } from "@/components/ui/EmptyState";
import { Flyout } from "@/components/ui/Flyout";
import { Input } from "@/components/ui/Input";
import { Mi } from "@/components/ui/icons";
import { ApiError } from "@/lib/api/client";
import type { Project, QuotaLimits } from "@/lib/api/generated/types";
import {
  useAdminTenantQuota,
  useProjectQuota,
  usePutAdminTenantQuota,
  usePutProjectQuota,
  useTenantQuota,
} from "@/lib/api/quota";
import { useMe, useResources } from "@/lib/api/queries";
import { useCreateProject, useDeleteProject, useProjects, useRenameProject } from "@/lib/api/tenant";
import { useActiveTenantId } from "@/lib/stores/uiStore";
import { relativeTime } from "@/lib/format";

/** Client-side preview only — the backend derives and collision-suffixes the real slug. */
function slugify(name: string): string {
  return name
    .toLowerCase()
    .trim()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

function errText(err: unknown): string {
  if (err instanceof ApiError) return err.detail;
  if (err instanceof Error) return err.message;
  return "Request failed.";
}

// ── Create ───────────────────────────────────────────────────────────────────

function CreateProjectFlyout({ onClose }: { onClose: () => void }) {
  const create = useCreateProject();
  const [name, setName] = useState("");
  const [error, setError] = useState("");
  const trimmed = name.trim();

  return (
    <Flyout title="Create project" onClose={onClose}>
      <p className="mb-4 text-[13px] leading-[1.5] text-ink-2">
        A project groups resources like an Azure resource group. A Proxmox pool is created for it
        automatically.
      </p>
      <div className="mb-[6px] text-[13px]">Project name</div>
      <Input
        value={name}
        onChange={(e) => {
          setName(e.target.value);
          setError("");
        }}
        placeholder="e.g. Web platform"
        aria-label="Project name"
        className="w-full"
        autoFocus
      />
      <p className="mt-2 text-[12px] text-ink-2">
        Slug preview: <span className="font-mono text-ink">{slugify(trimmed) || "—"}</span>
      </p>
      {error ? <p className="mt-3 text-[12px] text-err-text">{error}</p> : null}
      <div className="mt-5 flex gap-2">
        <Button
          variant="primary"
          disabled={trimmed === "" || create.isPending}
          onClick={() =>
            create.mutate(trimmed, {
              onSuccess: () => onClose(),
              onError: (err) => setError(errText(err)),
            })
          }
        >
          {create.isPending ? "Creating…" : "Create"}
        </Button>
        <Button variant="secondary" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </Flyout>
  );
}

// ── Rename ───────────────────────────────────────────────────────────────────

function RenameProjectFlyout({ project, onClose }: { project: Project; onClose: () => void }) {
  const rename = useRenameProject();
  const [name, setName] = useState(project.name);
  const [error, setError] = useState("");
  const trimmed = name.trim();

  return (
    <Flyout title="Rename project" onClose={onClose}>
      <p className="mb-4 text-[13px] leading-[1.5] text-ink-2">
        Renaming changes the display name only. The slug and Proxmox pool ({project.poolId}) stay the
        same.
      </p>
      <div className="mb-[6px] text-[13px]">Project name</div>
      <Input
        value={name}
        onChange={(e) => {
          setName(e.target.value);
          setError("");
        }}
        aria-label="Project name"
        className="w-full"
        autoFocus
      />
      {error ? <p className="mt-3 text-[12px] text-err-text">{error}</p> : null}
      <div className="mt-5 flex gap-2">
        <Button
          variant="primary"
          disabled={trimmed === "" || trimmed === project.name || rename.isPending}
          onClick={() =>
            rename.mutate(
              { id: project.id, name: trimmed },
              { onSuccess: () => onClose(), onError: (err) => setError(errText(err)) },
            )
          }
        >
          {rename.isPending ? "Saving…" : "Save"}
        </Button>
        <Button variant="secondary" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </Flyout>
  );
}

// ── Delete ───────────────────────────────────────────────────────────────────

function DeleteProjectFlyout({
  project,
  resourceCount,
  countKnown,
  onClose,
}: {
  project: Project;
  resourceCount: number;
  countKnown: boolean;
  onClose: () => void;
}) {
  const del = useDeleteProject();
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const empty = countKnown && resourceCount === 0;
  const match = text === project.name;

  return (
    <Flyout title="Delete project" onClose={onClose}>
      <div className="mb-4 flex gap-[10px] rounded-fluent border border-err bg-err-bg px-3 py-[10px] text-[13px] leading-[1.5]">
        <Mi name="warn" size={16} color="var(--color-err)" style={{ flexShrink: 0, marginTop: 2 }} />
        <span>
          Deleting <strong>{project.name}</strong> removes its Proxmox pool ({project.poolId}). This
          cannot be undone.
        </span>
      </div>

      {!countKnown ? (
        <p className="mb-4 text-[13px] text-ink-2">Checking whether the project is empty…</p>
      ) : !empty ? (
        <p className="mb-4 text-[13px] text-err-text">
          This project still owns {resourceCount} resource{resourceCount === 1 ? "" : "s"}. Move or
          delete them first — a project can only be deleted when empty.
        </p>
      ) : null}

      <div className="mb-[6px] text-[13px]">
        Type <strong>{project.name}</strong> to confirm
      </div>
      <Input
        value={text}
        onChange={(e) => {
          setText(e.target.value);
          setError("");
        }}
        placeholder={project.name}
        aria-label="Confirm project name"
        className="w-full"
        disabled={!empty}
      />
      {error ? <p className="mt-3 text-[12px] text-err-text">{error}</p> : null}

      <div className="mt-5 flex gap-2">
        <Button
          variant="danger"
          disabled={!empty || !match || del.isPending}
          onClick={() =>
            del.mutate(
              { id: project.id, confirmName: text },
              { onSuccess: () => onClose(), onError: (err) => setError(errText(err)) },
            )
          }
        >
          {del.isPending ? "Deleting…" : "Delete"}
        </Button>
        <Button variant="secondary" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </Flyout>
  );
}

// ── Quota: directory (tenant) ────────────────────────────────────────────────

/** Platform-admin editor for the tenant-wide quota. */
function TenantQuotaEditor({ onClose }: { onClose: () => void }) {
  const q = useAdminTenantQuota();
  const put = usePutAdminTenantQuota();
  const [serverError, setServerError] = useState("");

  if (q.isPending || q.isError || !q.data) {
    return (
      <Flyout title="Edit directory quota" onClose={onClose}>
        {q.isError ? (
          <>
            <CardError err={q.error} />
            <div className="mt-3">
              <Button variant="secondaryCompact" onClick={() => q.refetch()}>
                Retry
              </Button>
            </div>
          </>
        ) : (
          <Skeleton className="h-40" />
        )}
      </Flyout>
    );
  }

  return (
    <QuotaEditorFlyout
      title="Edit directory quota"
      intro="Platform-admin only. Tenant-wide caps across all projects; leave a field blank for unlimited."
      current={q.data.limits}
      pending={put.isPending}
      serverError={serverError}
      onSubmit={(body) =>
        put.mutate(body, { onSuccess: onClose, onError: (err) => setServerError(errText(err)) })
      }
      onClose={onClose}
    />
  );
}

function DirectoryQuotaSection({ isAdmin }: { isAdmin: boolean }) {
  const quota = useTenantQuota();
  const [editing, setEditing] = useState(false);
  return (
    <div className="mb-4 max-w-[560px]">
      <QuotaBarsCard
        title="Directory quota"
        subtitle="Tenant-wide limits and live usage across all projects."
        query={quota}
        action={
          isAdmin ? (
            <Button variant="secondaryCompact" disabled={quota.isPending} onClick={() => setEditing(true)}>
              Edit
            </Button>
          ) : undefined
        }
      />
      {editing ? <TenantQuotaEditor onClose={() => setEditing(false)} /> : null}
    </div>
  );
}

// ── Quota: per-project ───────────────────────────────────────────────────────

function ProjectQuotaEditor({
  project,
  current,
  tenantLimits,
  onClose,
}: {
  project: Project;
  current: QuotaLimits;
  tenantLimits: QuotaLimits;
  onClose: () => void;
}) {
  const put = usePutProjectQuota();
  const [serverError, setServerError] = useState("");
  return (
    <QuotaEditorFlyout
      title={`Quota — ${project.name}`}
      intro="Subdivide the directory's capacity for this project. Each limit must stay within the tenant limit; leave a field blank for unlimited."
      current={current}
      tenantLimits={tenantLimits}
      pending={put.isPending}
      serverError={serverError}
      onSubmit={(body) =>
        put.mutate(
          { projectId: project.id, body },
          { onSuccess: onClose, onError: (err) => setServerError(errText(err)) },
        )
      }
      onClose={onClose}
    />
  );
}

/** Expanded-row body: the project's quota bars + Owner "Edit quota". */
function ProjectQuotaPanel({ project, canManage }: { project: Project; canManage: boolean }) {
  const quota = useProjectQuota(project.id);
  const [editing, setEditing] = useState(false);
  return (
    <div className="bg-canvas px-4 py-4">
      <div className="flex items-start justify-between gap-4">
        <div className="w-full max-w-[560px]">
          {quota.isPending ? (
            <div className="space-y-3" aria-hidden>
              {[0, 1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-6" />
              ))}
            </div>
          ) : quota.isError ? (
            <div>
              <CardError err={quota.error} />
              <div className="mt-2">
                <Button variant="secondaryCompact" onClick={() => quota.refetch()}>
                  Retry
                </Button>
              </div>
            </div>
          ) : quota.data ? (
            <>
              {allUnlimited(quota.data.project.limits) ? (
                <p className="mb-2 text-[12px] text-ink-2">
                  No project limits set — usage is bounded only by the directory quota.
                </p>
              ) : null}
              <QuotaBars quota={quota.data.project} />
            </>
          ) : null}
        </div>
        {canManage ? (
          <Button variant="secondaryCompact" disabled={!quota.data} onClick={() => setEditing(true)}>
            Edit quota
          </Button>
        ) : null}
      </div>
      {editing && quota.data ? (
        <ProjectQuotaEditor
          project={project}
          current={quota.data.project.limits}
          tenantLimits={quota.data.tenant.limits}
          onClose={() => setEditing(false)}
        />
      ) : null}
    </div>
  );
}

// ── Page ─────────────────────────────────────────────────────────────────────

type Dialog =
  | { mode: "create" }
  | { mode: "rename"; project: Project }
  | { mode: "delete"; project: Project }
  | null;

export default function ProjectsPage() {
  const me = useMe();
  const activeTenantId = useActiveTenantId();
  const projects = useProjects();
  const resources = useResources();
  const [dialog, setDialog] = useState<Dialog>(null);
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set());

  const isAdmin = !!me.data?.isPlatformAdmin;
  const tenantRole = me.data?.tenants.find((t) => t.id === activeTenantId)?.role;
  const canManage = isAdmin || tenantRole === "owner";

  const toggleExpanded = (id: string) =>
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  // Per-project active-resource count, used to gate deletion (empty-only).
  const countKnown = !resources.isPending && !resources.isError;
  const countByProject = useMemo(() => {
    const m: Record<string, number> = {};
    for (const g of resources.data ?? []) {
      if (g.projectId) m[g.projectId] = (m[g.projectId] ?? 0) + 1;
    }
    return m;
  }, [resources.data]);

  return (
    <div className="max-w-[1200px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">Projects</span>
      </nav>
      <h1 className="mb-1 text-[24px] font-semibold">Projects</h1>
      <p className="mb-3 text-[12px] text-ink-2">
        Resource groups in your directory — each backed by a Proxmox pool. Resources are created into
        a project from the wizard.
      </p>

      <DirectoryQuotaSection isAdmin={isAdmin} />

      <div className="mb-3 flex items-center border-b border-line">
        <button
          type="button"
          disabled={!canManage}
          onClick={() => setDialog({ mode: "create" })}
          className="flex h-9 items-center gap-[6px] border-none bg-transparent px-[10px] text-[13px] text-ink enabled:cursor-pointer enabled:hover:bg-hover disabled:cursor-default disabled:text-ink-3"
          title={canManage ? "Create a project" : "Only owners can create projects"}
        >
          <Mi name="plus" size={14} color={canManage ? "var(--color-accent)" : "var(--color-ink-3)"} />
          Create project
        </button>
        <button
          type="button"
          onClick={() => projects.refetch()}
          className="flex h-9 cursor-pointer items-center gap-[6px] border-none bg-transparent px-[10px] text-[13px] text-ink hover:bg-hover"
        >
          <Mi name="restart" size={14} />
          Refresh
        </button>
      </div>

      <Card>
        {projects.isPending ? (
          <div className="space-y-2 p-4">
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
          </div>
        ) : projects.isError ? (
          <div className="p-4">
            <CardError err={projects.error} />
            <div className="mt-3">
              <Button variant="secondaryCompact" onClick={() => projects.refetch()}>
                Retry
              </Button>
            </div>
          </div>
        ) : (projects.data ?? []).length === 0 ? (
          <EmptyState
            icon="grid"
            title="No projects yet"
            body={
              canManage
                ? "Create your first project to start organizing resources into resource groups."
                : "No projects have been created in this directory yet."
            }
            cta={canManage ? { label: "Create project", onClick: () => setDialog({ mode: "create" }) } : undefined}
          />
        ) : (
          <table className="w-full border-collapse text-[13px]">
            <thead>
              <tr>
                {["", "Name", "Slug", "Pool", "Resources", "Created", ""].map((h, i) => (
                  <th
                    key={h || `col-${i}`}
                    className={`border-b border-line bg-hover px-4 py-2 font-semibold ${
                      i === 6 ? "text-right" : "text-left"
                    }`}
                  >
                    {h}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {(projects.data ?? []).map((p) => {
                const count = countByProject[p.id] ?? 0;
                const open = expanded.has(p.id);
                return (
                  <Fragment key={p.id}>
                    <tr className="border-b border-line-row last:border-b-0">
                      <td className="w-8 px-2">
                        <button
                          type="button"
                          onClick={() => toggleExpanded(p.id)}
                          aria-expanded={open}
                          aria-label={open ? `Hide quota for ${p.name}` : `Show quota for ${p.name}`}
                          className="flex h-6 w-6 cursor-pointer items-center justify-center rounded-fluent text-ink-2 hover:bg-hover"
                        >
                          <Mi name={open ? "chevronDown" : "chevronLeft"} size={14} color="currentColor" />
                        </button>
                      </td>
                      <td className="h-10 px-4 font-semibold">{p.name}</td>
                      <td className="px-4 font-mono text-ink-2">{p.slug}</td>
                      <td className="px-4 font-mono text-ink-2">{p.poolId}</td>
                      <td className="px-4 text-ink-2 tabular-nums">{countKnown ? count : "…"}</td>
                      <td className="px-4 text-ink-2 tabular-nums">{relativeTime(p.createdAt)}</td>
                      <td className="px-4 text-right">
                        {canManage ? (
                          <span className="inline-flex gap-1">
                            <Button variant="link" onClick={() => setDialog({ mode: "rename", project: p })}>
                              Rename
                            </Button>
                            <span className="text-line" aria-hidden>
                              ·
                            </span>
                            <Button variant="link" onClick={() => setDialog({ mode: "delete", project: p })}>
                              Delete
                            </Button>
                          </span>
                        ) : (
                          <span className="text-ink-3">—</span>
                        )}
                      </td>
                    </tr>
                    {open ? (
                      <tr className="border-b border-line-row last:border-b-0">
                        <td colSpan={7} className="p-0">
                          <ProjectQuotaPanel project={p} canManage={canManage} />
                        </td>
                      </tr>
                    ) : null}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        )}
      </Card>

      {dialog?.mode === "create" ? <CreateProjectFlyout onClose={() => setDialog(null)} /> : null}
      {dialog?.mode === "rename" ? (
        <RenameProjectFlyout project={dialog.project} onClose={() => setDialog(null)} />
      ) : null}
      {dialog?.mode === "delete" ? (
        <DeleteProjectFlyout
          project={dialog.project}
          resourceCount={countByProject[dialog.project.id] ?? 0}
          countKnown={countKnown}
          onClose={() => setDialog(null)}
        />
      ) : null}
    </div>
  );
}
