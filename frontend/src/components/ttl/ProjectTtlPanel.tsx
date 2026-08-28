"use client";
// Project TTL section (ADR-0020): the "expiring soon / expired" view — the
// project's TTLs, ordered by expiry (the backend orders them), each a VMID +
// action + live countdown/expired chip — plus an Owner-gated TTL-policy editor
// (default + max) in a QuotaEditorFlyout-style flyout. Reader sees the list
// read-only. Loading / empty / error states on both the list and the editor.
import { useMemo, useState } from "react";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { TtlBadge } from "@/components/ttl/TtlBadge";
import { Button } from "@/components/ui/Button";
import { Flyout } from "@/components/ui/Flyout";
import { Input } from "@/components/ui/Input";
import type { Project, TtlPolicyRequest } from "@/lib/api/generated/types";
import { useProjectTtlPolicy, useProjectTtls, usePutProjectTtlPolicy } from "@/lib/api/ttlQueries";
import { ApiError } from "@/lib/api/client";
import { formatTtlDuration } from "@/lib/ttl";

const HOUR = 3600;

function errText(err: unknown): string {
  if (err instanceof ApiError) return err.detail;
  if (err instanceof Error) return err.message;
  return "Request failed.";
}

// ── Policy editor ─────────────────────────────────────────────────────────────

interface PolicyValidation {
  errors: { maxHours?: string; defaultHours?: string };
  body?: TtlPolicyRequest;
}

/** Pure client validation for the policy flyout. maxHours is required (> 0);
 *  defaultHours is optional (blank ⇒ no default ⇒ permanent) and must be ≤ max.
 *  Exported for unit tests. */
export function validateTtlPolicyForm(maxHours: string, defaultHours: string): PolicyValidation {
  const errors: PolicyValidation["errors"] = {};
  const max = Number(maxHours);
  if (!Number.isInteger(max) || max <= 0) {
    errors.maxHours = "Enter a whole number of hours greater than zero.";
  }

  const defRaw = defaultHours.trim();
  let defSeconds: number | null = null;
  if (defRaw !== "") {
    const def = Number(defRaw);
    if (!Number.isInteger(def) || def <= 0) {
      errors.defaultHours = "Enter a whole number of hours, or leave blank for no default.";
    } else if (Number.isInteger(max) && max > 0 && def > max) {
      errors.defaultHours = "The default cannot exceed the maximum.";
    } else {
      defSeconds = def * HOUR;
    }
  }

  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    body: { maxTtlSeconds: max * HOUR, defaultTtlSeconds: defSeconds ?? undefined },
  };
}

function PolicyFlyout({ project, onClose }: { project: Project; onClose: () => void }) {
  const policy = useProjectTtlPolicy(project.id);
  const put = usePutProjectTtlPolicy(project.id);
  const [maxHours, setMaxHours] = useState("");
  const [defaultHours, setDefaultHours] = useState("");
  const [seeded, setSeeded] = useState(false);
  const [serverError, setServerError] = useState("");

  // Seed once from the loaded policy (in hours).
  if (!seeded && policy.data) {
    setMaxHours(String(Math.max(1, Math.round(policy.data.maxTtlSeconds / HOUR))));
    setDefaultHours(
      policy.data.defaultTtlSeconds ? String(Math.round(policy.data.defaultTtlSeconds / HOUR)) : "",
    );
    setSeeded(true);
  }

  const { errors, body } = validateTtlPolicyForm(maxHours, defaultHours);

  return (
    <Flyout
      title={`TTL policy — ${project.name}`}
      onClose={onClose}
      footer={
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="primary"
            disabled={!body || put.isPending || !policy.data}
            onClick={() =>
              body &&
              put.mutate(body, {
                onSuccess: onClose,
                onError: (err) => setServerError(errText(err)),
              })
            }
          >
            {put.isPending ? "Saving…" : "Save"}
          </Button>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
        </div>
      }
    >
      <p className="mb-4 text-[13px] leading-[1.5] text-ink-2">
        Governs TTLs for guests in <strong>{project.name}</strong>. The maximum caps any TTL at
        create or extend; the default is applied when a guest opts into a TTL without choosing a
        length. Leave the default blank so guests are permanent unless explicitly given a TTL.
      </p>

      {policy.isPending ? (
        <Skeleton className="h-40" />
      ) : policy.isError ? (
        <>
          <CardError err={policy.error} />
          <div className="mt-3">
            <Button variant="secondaryCompact" onClick={() => policy.refetch()}>
              Retry
            </Button>
          </div>
        </>
      ) : (
        <>
          <div className="mb-4">
            <label htmlFor="ttl-max" className="mb-[6px] block text-[13px] font-semibold">
              Maximum TTL (hours)
            </label>
            <Input
              id="ttl-max"
              value={maxHours}
              onChange={(e) => {
                setMaxHours(e.target.value);
                setServerError("");
              }}
              invalid={!!errors.maxHours}
              inputMode="numeric"
              className="w-[160px]"
              aria-label="Maximum TTL hours"
            />
            {errors.maxHours ? (
              <p className="mt-1 text-[12px] text-err-text">{errors.maxHours}</p>
            ) : (
              <p className="mt-1 text-[12px] text-ink-3">
                Hard ceiling on any TTL in this project.
              </p>
            )}
          </div>

          <div className="mb-2">
            <label htmlFor="ttl-default" className="mb-[6px] block text-[13px] font-semibold">
              Default TTL (hours)
            </label>
            <Input
              id="ttl-default"
              value={defaultHours}
              onChange={(e) => {
                setDefaultHours(e.target.value);
                setServerError("");
              }}
              invalid={!!errors.defaultHours}
              inputMode="numeric"
              placeholder="none"
              className="w-[160px]"
              aria-label="Default TTL hours"
            />
            {errors.defaultHours ? (
              <p className="mt-1 text-[12px] text-err-text">{errors.defaultHours}</p>
            ) : (
              <p className="mt-1 text-[12px] text-ink-3">
                Blank ⇒ no default (guests are permanent).
              </p>
            )}
          </div>

          {serverError ? <p className="mt-3 text-[12px] text-err-text">{serverError}</p> : null}
        </>
      )}
    </Flyout>
  );
}

// ── Section (list + editor trigger) ────────────────────────────────────────────

export function ProjectTtlSection({
  project,
  canManage,
}: {
  project: Project;
  canManage: boolean;
}) {
  const ttls = useProjectTtls(project.id);
  const policy = useProjectTtlPolicy(project.id);
  const [editing, setEditing] = useState(false);

  const rows = useMemo(() => ttls.data ?? [], [ttls.data]);

  return (
    <div className="mt-4 border-t border-line pt-4">
      <div className="flex items-start justify-between gap-4">
        <div className="max-w-[560px]">
          <div className="text-[13px] font-semibold">Ephemeral resources (TTL)</div>
          <p className="mt-1 text-[12px] text-ink-2">
            {policy.isPending ? (
              "Loading policy…"
            ) : policy.data ? (
              <>
                Max TTL {formatTtlDuration(policy.data.maxTtlSeconds)}
                {policy.data.defaultTtlSeconds
                  ? ` · default ${formatTtlDuration(policy.data.defaultTtlSeconds)}`
                  : " · no default"}
                .
              </>
            ) : (
              "Guests with a TTL expire and are stopped or deleted automatically."
            )}
          </p>
        </div>
        {canManage ? (
          <Button
            variant="secondaryCompact"
            disabled={policy.isPending}
            onClick={() => setEditing(true)}
          >
            Edit TTL policy
          </Button>
        ) : null}
      </div>

      <div className="mt-3">
        {ttls.isPending ? (
          <Skeleton className="h-16" />
        ) : ttls.isError ? (
          <div>
            <CardError err={ttls.error} />
            <div className="mt-2">
              <Button variant="secondaryCompact" onClick={() => ttls.refetch()}>
                Retry
              </Button>
            </div>
          </div>
        ) : rows.length === 0 ? (
          <p className="text-[12px] text-ink-2">No guests in this project have a TTL.</p>
        ) : (
          <ul className="space-y-[6px]">
            {rows.map((t) => (
              <li key={t.id} className="flex items-center gap-3 text-[13px]">
                <span className="w-16 font-mono text-ink-2 tabular-nums">VMID {t.vmid}</span>
                <span className="w-20 text-ink-2">{t.action === "delete" ? "Delete" : "Stop"}</span>
                <TtlBadge ttl={t} />
              </li>
            ))}
          </ul>
        )}
      </div>

      {editing ? <PolicyFlyout project={project} onClose={() => setEditing(false)} /> : null}
    </div>
  );
}
