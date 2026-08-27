"use client";
// Lifecycle (TTL) blade — ephemeral resources (ADR-0020). A structured editor
// over the guest's TTL: a duration (preset or custom hours) and an action —
// "stop" (reversible power-off) or "delete" (irreversible destroy, which
// requires typing the guest name, exactly like the guest-delete flyout).
// Contributor+ can edit; a Reader sees the same fields, read-only. GET 404 ⇒
// "No TTL — this guest is permanent" empty state (not an error). Apply = PUT;
// Extend = POST extend (when a TTL exists); Remove TTL = DELETE (confirmed).
// The project TTL policy caps the duration and seeds the default.
import { useEffect, useMemo, useState } from "react";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { BladeHeading, useGuestParams } from "@/components/guest/common";
import { TtlBadge } from "@/components/ttl/TtlBadge";
import {
  TtlEditor,
  defaultTtlForm,
  ttlToForm,
  validateTtlForm,
  type TtlFormValues,
} from "@/components/ttl/TtlEditor";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/ui/EmptyState";
import { useGuest } from "@/lib/api/guestQueries";
import { useMe, useResources } from "@/lib/api/queries";
import {
  useDeleteGuestTtl,
  useExtendGuestTtl,
  useGuestTtl,
  useProjectTtlPolicy,
  usePutGuestTtl,
} from "@/lib/api/ttlQueries";
import { formatDateTime } from "@/lib/format";
import { useActiveTenantId } from "@/lib/stores/uiStore";

export default function GuestTtlPage() {
  const g = useGuestParams();
  const guest = useGuest(g);
  const ttl = useGuestTtl(g);
  const put = usePutGuestTtl(g);
  const del = useDeleteGuestTtl(g);
  const extend = useExtendGuestTtl(g);

  const me = useMe();
  const activeTenantId = useActiveTenantId();
  const role = me.data?.tenants.find((t) => t.id === activeTenantId)?.role;
  const canEdit = !!me.data?.isPlatformAdmin || role === "owner" || role === "contributor";

  // GuestDetail carries no projectId, so resolve the owning project from the
  // resources list (GuestSummary.projectId) — the TTL policy is project-scoped.
  const resources = useResources();
  const projectId = useMemo(
    () =>
      (resources.data ?? []).find(
        (r) => r.vmid === g.vmid && r.type === g.type && r.node === g.node,
      )?.projectId,
    [resources.data, g.vmid, g.type, g.node],
  );
  const policy = useProjectTtlPolicy(projectId);

  const guestName = guest.data?.name ?? `VMID ${g.vmid}`;
  const maxTtlSeconds = policy.data?.maxTtlSeconds ?? 0;

  const [form, setForm] = useState<TtlFormValues | null>(null);
  const [confirmRemove, setConfirmRemove] = useState(false);

  // Seed the form from the stored TTL (or clear it) whenever the guest or the
  // stored TTL identity changes. With no TTL the form stays null and the empty
  // state shows until the user chooses to set one — the id-keyed deps mean
  // clicking "Set TTL" is not clobbered by a refetch.
  useEffect(() => {
    setForm(ttl.data ? ttlToForm(ttl.data) : null);
    setConfirmRemove(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ttl.data?.id, g.node, g.type, g.vmid]);

  const patch = (p: Partial<TtlFormValues>) => setForm((f) => (f ? { ...f, ...p } : f));

  const { errors, body } = useMemo(
    () =>
      form && policy.data
        ? validateTtlForm(form, { maxTtlSeconds, guestName })
        : { errors: {}, body: undefined },
    [form, policy.data, maxTtlSeconds, guestName],
  );

  const baseline = ttl.data ? ttlToForm(ttl.data) : null;
  const dirty = !!form && (!baseline || JSON.stringify(form) !== JSON.stringify(baseline));

  // TTL + policy must both resolve before the editor is meaningful.
  if (ttl.isPending || policy.isPending || resources.isPending) return <Skeleton className="h-64" />;
  if (ttl.isError) return <CardError err={ttl.error} />;
  if (policy.isError) return <CardError err={policy.error} />;

  // No project ownership row → no policy to enforce; surface it honestly.
  if (!projectId) {
    return (
      <div className="max-w-[640px]">
        <BladeHeading>Lifecycle</BladeHeading>
        <EmptyState
          icon="bolt"
          title="No project"
          body="This guest is not assigned to a project, so no TTL policy applies. Assign it to a project to set an expiry."
        />
      </div>
    );
  }

  // No TTL set, and the user hasn't started creating one → empty state.
  if (form === null) {
    return (
      <div className="max-w-[640px]">
        <BladeHeading>Lifecycle</BladeHeading>
        <p className="mb-4 -mt-1 text-[12px] text-ink-2">
          A TTL makes this guest ephemeral: when it expires the guest is either powered off
          (reversible) or permanently deleted. It stays running indefinitely without one.
        </p>
        <EmptyState
          icon="bolt"
          title="No TTL — this guest is permanent"
          body={
            canEdit
              ? "This guest has no expiry. Set a TTL to have it stop or be deleted automatically."
              : "This guest has no expiry set."
          }
          cta={
            canEdit
              ? { label: "Set TTL", onClick: () => setForm(defaultTtlForm(policy.data?.defaultTtlSeconds)) }
              : undefined
          }
        />
      </div>
    );
  }

  return (
    <div className="max-w-[640px]">
      <BladeHeading>Lifecycle</BladeHeading>
      <p className="mb-3 -mt-1 text-[12px] text-ink-2">
        {canEdit
          ? "Time-to-live for this guest. Changes take effect on the next scheduler tick."
          : "Time-to-live for this guest (read-only — you don't have permission to change it)."}
      </p>

      {ttl.data ? (
        <div className="mb-4 flex flex-wrap items-center gap-2 text-[13px]">
          <TtlBadge ttl={ttl.data} />
          <span className="text-ink-2">Expires {formatDateTime(ttl.data.expiresAt)}</span>
        </div>
      ) : null}

      <TtlEditor
        values={form}
        onChange={patch}
        errors={errors}
        maxTtlSeconds={maxTtlSeconds}
        guestName={guestName}
        disabled={!canEdit}
      />

      {canEdit ? (
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <Button
            variant="primary"
            disabled={!dirty || !body || put.isPending}
            onClick={() => body && put.mutate(body)}
          >
            {put.isPending ? "Applying…" : "Apply changes"}
          </Button>

          {ttl.data ? (
            <>
              <Button variant="secondary" disabled={extend.isPending} onClick={() => extend.mutate()}>
                {extend.isPending ? "Extending…" : "Extend"}
              </Button>

              {confirmRemove ? (
                <span className="inline-flex items-center gap-2 text-[13px]">
                  <span className="text-ink-2">Remove this TTL?</span>
                  <Button variant="danger" disabled={del.isPending} onClick={() => del.mutate()}>
                    {del.isPending ? "Removing…" : "Confirm remove"}
                  </Button>
                  <Button variant="secondary" onClick={() => setConfirmRemove(false)}>
                    Cancel
                  </Button>
                </span>
              ) : (
                <Button variant="secondary" onClick={() => setConfirmRemove(true)}>
                  Remove TTL
                </Button>
              )}
            </>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
