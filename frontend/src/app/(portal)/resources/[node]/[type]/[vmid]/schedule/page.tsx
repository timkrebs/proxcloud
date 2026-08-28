"use client";
// Schedule blade — auto-shutdown (ADR-0019). A structured editor over the guest's
// schedule: shutdown time, optional auto-start, days-of-week, timezone, grace,
// enabled, and an opt-out from the inherited project schedule. Contributor+ can
// edit; a Reader gets the same fields, read-only. GET 404 ⇒ "No schedule" empty
// state (not an error). Apply = PUT; Remove = DELETE (confirmed); Skip = POST skip.
import { useEffect, useMemo, useState } from "react";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { BladeHeading, useGuestParams } from "@/components/guest/common";
import { ScheduleBadge } from "@/components/schedule/ScheduleBadge";
import {
  ScheduleEditor,
  defaultScheduleForm,
  scheduleToForm,
  validateScheduleForm,
  type ScheduleFormValues,
} from "@/components/schedule/ScheduleEditor";
import { resolveDefaultTimezone } from "@/components/schedule/TimezonePicker";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/ui/EmptyState";
import { useMe } from "@/lib/api/queries";
import {
  useDeleteResourceSchedule,
  usePutResourceSchedule,
  useResourceSchedule,
  useSkipSchedule,
} from "@/lib/api/scheduleQueries";
import { useActiveTenantId } from "@/lib/stores/uiStore";

export default function GuestSchedulePage() {
  const g = useGuestParams();
  const sched = useResourceSchedule(g);
  const put = usePutResourceSchedule(g);
  const del = useDeleteResourceSchedule(g);
  const skip = useSkipSchedule(g);

  const me = useMe();
  const activeTenantId = useActiveTenantId();
  const role = me.data?.tenants.find((t) => t.id === activeTenantId)?.role;
  const canEdit = !!me.data?.isPlatformAdmin || role === "owner" || role === "contributor";

  const browserTz = useMemo(() => resolveDefaultTimezone(), []);
  const [form, setForm] = useState<ScheduleFormValues | null>(null);
  const [confirmRemove, setConfirmRemove] = useState(false);

  // Seed the form from the stored schedule (or clear it) whenever the guest or
  // the stored schedule identity changes. When there is no schedule, the form
  // stays null and the empty state is shown until the user chooses to set one —
  // the id-keyed deps mean clicking "Set schedule" is not clobbered by a refetch.
  useEffect(() => {
    setForm(sched.data ? scheduleToForm(sched.data) : null);
    setConfirmRemove(false);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sched.data?.id, g.node, g.type, g.vmid]);

  const patch = (p: Partial<ScheduleFormValues>) => setForm((f) => (f ? { ...f, ...p } : f));

  const { errors, body } = useMemo(
    () => (form ? validateScheduleForm(form, "resource") : { errors: {}, body: undefined }),
    [form],
  );

  const baseline = sched.data ? scheduleToForm(sched.data) : null;
  const dirty = !!form && (!baseline || JSON.stringify(form) !== JSON.stringify(baseline));

  if (sched.isPending) return <Skeleton className="h-64" />;
  if (sched.isError) return <CardError err={sched.error} />;

  // No schedule set, and the user hasn't started creating one → empty state.
  if (form === null) {
    return (
      <div className="max-w-[640px]">
        <BladeHeading>Schedule</BladeHeading>
        <p className="mb-4 -mt-1 text-[12px] text-ink-2">
          Auto-shutdown powers this guest off on a weekly recurrence, warning it first. It can
          optionally power the guest back on later the same day.
        </p>
        <EmptyState
          icon="clock"
          title="No schedule"
          body={
            canEdit
              ? "This guest has no auto-shutdown schedule. It may still inherit one from its project."
              : "This guest has no auto-shutdown schedule of its own."
          }
          cta={
            canEdit
              ? { label: "Set schedule", onClick: () => setForm(defaultScheduleForm(browserTz)) }
              : undefined
          }
        />
      </div>
    );
  }

  return (
    <div className="max-w-[640px]">
      <BladeHeading>Schedule</BladeHeading>
      <p className="mb-3 -mt-1 text-[12px] text-ink-2">
        {canEdit
          ? "Auto-shutdown for this guest. Changes take effect on the next scheduler tick."
          : "Auto-shutdown for this guest (read-only — you don't have permission to change it)."}
      </p>

      {sched.data ? (
        <div className="mb-4">
          <ScheduleBadge schedule={sched.data} />
        </div>
      ) : null}

      <ScheduleEditor
        values={form}
        onChange={patch}
        errors={errors}
        scope="resource"
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

          {sched.data ? (
            <>
              <Button variant="secondary" disabled={skip.isPending} onClick={() => skip.mutate()}>
                {skip.isPending ? "Skipping…" : "Skip next occurrence"}
              </Button>

              {confirmRemove ? (
                <span className="inline-flex items-center gap-2 text-[13px]">
                  <span className="text-ink-2">Remove this schedule?</span>
                  <Button variant="danger" disabled={del.isPending} onClick={() => del.mutate()}>
                    {del.isPending ? "Removing…" : "Confirm remove"}
                  </Button>
                  <Button variant="secondary" onClick={() => setConfirmRemove(false)}>
                    Cancel
                  </Button>
                </span>
              ) : (
                <Button variant="secondary" onClick={() => setConfirmRemove(true)}>
                  Remove schedule
                </Button>
              )}
            </>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}
