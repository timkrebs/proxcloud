"use client";
// Project auto-shutdown editor — the project-scope twin of the resource blade,
// in a QuotaEditorFlyout-style pane. Same structured fields minus opt-out. The
// project schedule is inherited by every guest in the project that does not set
// its own (or opt out). GET 404 ⇒ "no schedule" (the create flow), not an error.
import { useEffect, useMemo, useState } from "react";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import {
  ScheduleEditor,
  defaultScheduleForm,
  scheduleToForm,
  validateScheduleForm,
  type ScheduleFormValues,
} from "@/components/schedule/ScheduleEditor";
import { resolveDefaultTimezone } from "@/components/schedule/TimezonePicker";
import { Button } from "@/components/ui/Button";
import { Flyout } from "@/components/ui/Flyout";
import type { Project } from "@/lib/api/generated/types";
import {
  useDeleteProjectSchedule,
  useProjectSchedule,
  usePutProjectSchedule,
} from "@/lib/api/scheduleQueries";

export function ProjectScheduleFlyout({
  project,
  onClose,
}: {
  project: Project;
  onClose: () => void;
}) {
  const sched = useProjectSchedule(project.id);
  const put = usePutProjectSchedule(project.id);
  const del = useDeleteProjectSchedule(project.id);

  const browserTz = useMemo(() => resolveDefaultTimezone(), []);
  const [form, setForm] = useState<ScheduleFormValues | null>(null);
  const [confirmRemove, setConfirmRemove] = useState(false);

  useEffect(() => {
    if (sched.isSuccess) {
      setForm(sched.data ? scheduleToForm(sched.data) : defaultScheduleForm(browserTz));
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sched.isSuccess, sched.data?.id]);

  const patch = (p: Partial<ScheduleFormValues>) => setForm((f) => (f ? { ...f, ...p } : f));
  const { errors, body } = useMemo(
    () => (form ? validateScheduleForm(form, "project") : { errors: {}, body: undefined }),
    [form],
  );

  return (
    <Flyout
      title={`Schedule — ${project.name}`}
      onClose={onClose}
      footer={
        <div className="flex flex-wrap items-center gap-2">
          <Button
            variant="primary"
            disabled={!form || !body || put.isPending}
            onClick={() => body && put.mutate(body, { onSuccess: onClose })}
          >
            {put.isPending ? "Saving…" : "Save"}
          </Button>
          {sched.data ? (
            confirmRemove ? (
              <>
                <Button
                  variant="danger"
                  disabled={del.isPending}
                  onClick={() => del.mutate(undefined, { onSuccess: onClose })}
                >
                  {del.isPending ? "Removing…" : "Confirm remove"}
                </Button>
                <Button variant="secondary" onClick={() => setConfirmRemove(false)}>
                  Cancel
                </Button>
              </>
            ) : (
              <Button variant="secondary" onClick={() => setConfirmRemove(true)}>
                Remove
              </Button>
            )
          ) : (
            <Button variant="secondary" onClick={onClose}>
              Cancel
            </Button>
          )}
        </div>
      }
    >
      <p className="mb-4 text-[13px] leading-[1.5] text-ink-2">
        Applies to every guest in <strong>{project.name}</strong> that has no schedule of its own
        and has not opted out. A guest-level schedule always overrides this.
      </p>

      {sched.isPending ? (
        <Skeleton className="h-64" />
      ) : sched.isError ? (
        <>
          <CardError err={sched.error} />
          <div className="mt-3">
            <Button variant="secondaryCompact" onClick={() => sched.refetch()}>
              Retry
            </Button>
          </div>
        </>
      ) : form ? (
        <ScheduleEditor values={form} onChange={patch} errors={errors} scope="project" />
      ) : null}
    </Flyout>
  );
}
