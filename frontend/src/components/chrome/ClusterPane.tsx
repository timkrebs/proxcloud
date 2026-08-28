"use client";
// Directory + project pane — design-inventory §3.12 (tenant + project pane)
// mapped to the tenancy model: the TENANT is the isolation boundary (an Azure
// directory), PROJECTS are the resource-group scope filter. 400px flyout, radio
// rows, footer Done + Sign out.
import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { Button } from "@/components/ui/Button";
import { Flyout } from "@/components/ui/Flyout";
import type { Project, TenantMembership } from "@/lib/api/generated/types";

export interface TenantPaneProps {
  tenants: TenantMembership[];
  activeTenantId: string | null;
  /** Switch directories (PATCH active-tenant + rescope). No-op if already active. */
  onSelectTenant: (id: string) => void;
  /** True while the active-tenant switch is in flight (radios disabled). */
  switching: boolean;
  projects: Project[];
  projectsPending: boolean;
  projectsError: unknown;
  /** null = "All projects". */
  selectedProjectId: string | null;
  onSelectProject: (id: string | null) => void;
  onClose: () => void;
  onSignOut: () => void;
}

/** §4.7 radio: 16px circle, accent border + inner 8px accent dot when active. */
function Radio({ active }: { active: boolean }) {
  return (
    <span
      aria-hidden
      className={`flex h-4 w-4 shrink-0 items-center justify-center rounded-full border ${
        active ? "border-accent" : "border-line-input"
      }`}
    >
      {active ? <span className="h-2 w-2 rounded-full bg-accent" /> : null}
    </span>
  );
}

/** §3.12 section label: 12px/600, uppercase, letter-spacing .3px. */
function SectionLabel({ children }: { children: string }) {
  return (
    <div className="mb-[6px] text-[12px] font-semibold tracking-[.3px] text-ink-2 uppercase">
      {children}
    </div>
  );
}

function RadioRow({
  active,
  disabled,
  onClick,
  title,
  subtitle,
  meta,
  ariaLabel,
}: {
  active: boolean;
  disabled?: boolean;
  onClick: () => void;
  title: string;
  subtitle?: string;
  meta?: string;
  ariaLabel: string;
}) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={active}
      aria-label={ariaLabel}
      disabled={disabled}
      onClick={onClick}
      className={`flex w-full cursor-pointer items-center gap-[10px] rounded-fluent px-[10px] py-2 text-left disabled:cursor-default ${
        active ? "bg-selected" : "hover:bg-hover"
      }`}
    >
      <Radio active={active} />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-[13px] font-semibold">{title}</span>
        {subtitle ? (
          <span className="block truncate text-[11px] text-ink-2">{subtitle}</span>
        ) : null}
      </span>
      {meta ? <span className="flex-none text-[11px] text-ink-2 capitalize">{meta}</span> : null}
    </button>
  );
}

export function TenantPane({
  tenants,
  activeTenantId,
  onSelectTenant,
  switching,
  projects,
  projectsPending,
  projectsError,
  selectedProjectId,
  onSelectProject,
  onClose,
  onSignOut,
}: TenantPaneProps) {
  return (
    <Flyout
      title="Directory + project"
      onClose={onClose}
      footer={
        <div className="flex items-center justify-between">
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
          <Button variant="link" onClick={onSignOut}>
            Sign out
          </Button>
        </div>
      }
    >
      <p className="mb-[14px] text-[12px] leading-[1.5] text-ink-2">
        Your directory is the isolation boundary; a project scopes what you see — resources, usage,
        and activity.
      </p>

      <div role="radiogroup" aria-label="Directory">
        <SectionLabel>TENANTS</SectionLabel>
        {tenants.length === 0 ? (
          <p className="text-[12px] leading-[1.5] text-ink-2">
            You don&apos;t belong to any directory yet.
          </p>
        ) : (
          tenants.map((t) => (
            <RadioRow
              key={t.id}
              active={t.id === activeTenantId}
              disabled={switching}
              onClick={() => {
                if (t.id !== activeTenantId) onSelectTenant(t.id);
              }}
              title={t.name}
              subtitle={t.slug}
              meta={t.role || undefined}
              ariaLabel={`Directory ${t.name}`}
            />
          ))
        )}
      </div>

      <div className="my-[14px] h-px bg-line" />

      <div role="radiogroup" aria-label="Project">
        <SectionLabel>PROJECTS</SectionLabel>
        {projectsPending ? (
          <div className="space-y-2">
            <Skeleton className="h-8" />
            <Skeleton className="h-8" />
          </div>
        ) : projectsError ? (
          <CardError err={projectsError} />
        ) : (
          <>
            <RadioRow
              active={selectedProjectId === null}
              onClick={() => onSelectProject(null)}
              title="All projects"
              ariaLabel="All projects"
            />
            {projects.map((p) => (
              <RadioRow
                key={p.id}
                active={selectedProjectId === p.id}
                onClick={() => onSelectProject(p.id)}
                title={p.name}
                subtitle={p.slug}
                ariaLabel={`Project ${p.name}`}
              />
            ))}
            {projects.length === 0 ? (
              <p className="mt-1 text-[12px] leading-[1.5] text-ink-2">
                No projects in this directory yet.
              </p>
            ) : null}
          </>
        )}
      </div>
    </Flyout>
  );
}
