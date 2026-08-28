"use client";
// Snapshots blade — design §3.5.7: empty state with CTA, real snapshot
// table with rollback/delete (typed-name-free but confirmed inline).
import { useState } from "react";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import {
  BladeHeading,
  BladeTable,
  bladeCell,
  bladeCellMuted,
  useGuestParams,
} from "@/components/guest/common";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/ui/EmptyState";
import { Input } from "@/components/ui/Input";
import {
  useCreateSnapshot,
  useDeleteSnapshot,
  useGuestSnapshots,
  useRollbackSnapshot,
} from "@/lib/api/guestQueries";
import { formatDateTime } from "@/lib/format";

const NAME_RE = /^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,39}$/;

export default function GuestSnapshotsPage() {
  const g = useGuestParams();
  const snaps = useGuestSnapshots(g);
  const create = useCreateSnapshot(g);
  const rollback = useRollbackSnapshot(g);
  const remove = useDeleteSnapshot(g);
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [err, setErr] = useState("");
  const [confirm, setConfirm] = useState<{ action: "rollback" | "delete"; name: string } | null>(
    null,
  );

  const submit = () => {
    if (!NAME_RE.test(name)) {
      setErr("Start with a letter or digit; letters, digits, . - _ only (max 40).");
      return;
    }
    create.mutate(
      { name },
      {
        onSuccess: () => {
          setCreating(false);
          setName("");
        },
      },
    );
  };

  const startCreate = () => {
    setCreating(true);
    setName(`snap-${new Date().toISOString().slice(0, 10)}`);
    setErr("");
  };

  return (
    <div>
      <div className="mb-3 flex items-center justify-between">
        <BladeHeading>Snapshots</BladeHeading>
        {(snaps.data ?? []).length > 0 && !creating ? (
          <Button variant="primaryCompact" onClick={startCreate}>
            Take snapshot
          </Button>
        ) : null}
      </div>

      {creating ? (
        <div className="mb-4 flex items-start gap-2">
          <div>
            <Input
              value={name}
              onChange={(e) => {
                setName(e.target.value);
                setErr("");
              }}
              aria-label="Snapshot name"
              invalid={err !== ""}
              className="w-[260px]"
              autoFocus
            />
            {err ? <p className="mt-1 text-[12px] text-err-text">{err}</p> : null}
          </div>
          <Button variant="primaryCompact" disabled={create.isPending} onClick={submit}>
            Create
          </Button>
          <Button variant="secondaryCompact" onClick={() => setCreating(false)}>
            Cancel
          </Button>
        </div>
      ) : null}

      {snaps.isPending ? (
        <Skeleton className="h-32" />
      ) : snaps.isError ? (
        <CardError err={snaps.error} />
      ) : (snaps.data ?? []).length === 0 && !creating ? (
        <EmptyState
          icon="camera"
          title="No snapshots yet"
          body="Snapshots capture the guest's disks at a point in time so you can roll back after risky changes."
          cta={{ label: "Take your first snapshot", onClick: startCreate }}
        />
      ) : (snaps.data ?? []).length > 0 ? (
        <BladeTable headers={["Name", "Created", "Description", "RAM", ""]}>
          {(snaps.data ?? []).map((s) => (
            <tr key={s.name} className="border-b border-line-row last:border-b-0">
              <td className={bladeCell}>{s.name}</td>
              <td className={`${bladeCellMuted} tabular-nums`}>{formatDateTime(s.snapTime)}</td>
              <td className={bladeCellMuted}>{s.description || "—"}</td>
              <td className={bladeCellMuted}>{s.vmState ? "included" : "—"}</td>
              <td className={bladeCell}>
                {confirm?.name === s.name ? (
                  <span className="flex items-center gap-2 text-[12px]">
                    <span className="text-err-text">
                      {confirm.action === "rollback"
                        ? "Roll back to this snapshot? Current state is lost."
                        : "Delete this snapshot permanently?"}
                    </span>
                    <Button
                      variant="danger"
                      disabled={rollback.isPending || remove.isPending}
                      onClick={() => {
                        if (confirm.action === "rollback") rollback.mutate(s.name);
                        else remove.mutate(s.name);
                        setConfirm(null);
                      }}
                    >
                      Confirm
                    </Button>
                    <Button variant="secondaryCompact" onClick={() => setConfirm(null)}>
                      Cancel
                    </Button>
                  </span>
                ) : (
                  <span className="flex gap-3">
                    <Button
                      variant="link"
                      onClick={() => setConfirm({ action: "rollback", name: s.name })}
                    >
                      Roll back
                    </Button>
                    <Button
                      variant="link"
                      onClick={() => setConfirm({ action: "delete", name: s.name })}
                    >
                      Delete
                    </Button>
                  </span>
                )}
              </td>
            </tr>
          ))}
        </BladeTable>
      ) : null}
    </div>
  );
}
