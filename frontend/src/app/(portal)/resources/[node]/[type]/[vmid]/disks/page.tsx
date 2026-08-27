"use client";
// Disks blade — design §3.5.6 + resize (grow-only, absolute GiB).
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
import { Input } from "@/components/ui/Input";
import { useGuest, useResizeDisk } from "@/lib/api/guestQueries";
import { formatBytes } from "@/lib/format";

export default function GuestDisksPage() {
  const g = useGuestParams();
  const guest = useGuest(g);
  const resize = useResizeDisk(g);
  const [editing, setEditing] = useState<string | null>(null);
  const [sizeGib, setSizeGib] = useState("");
  const [err, setErr] = useState("");

  if (guest.isPending) return <Skeleton className="h-32" />;
  if (guest.isError) return <CardError err={guest.error} />;
  const disks = guest.data.disks;

  return (
    <div>
      <BladeHeading>Disks</BladeHeading>
      <p className="mb-3 -mt-1 text-[12px] text-ink-2">
        Disks can only grow — Proxmox does not shrink volumes.
      </p>
      {disks.length === 0 ? (
        <p className="text-[13px] text-ink-2">No disks configured.</p>
      ) : (
        <BladeTable headers={["Device", "Storage", "Volume", "Size", ""]}>
          {disks.map((disk) => {
            const currentGib = Math.round(disk.sizeBytes / 2 ** 30);
            return (
              <tr key={disk.key} className="border-b border-line-row last:border-b-0">
                <td className={bladeCell}>
                  {disk.key}
                  {disk.cdrom ? <span className="ml-2 text-[11px] text-ink-3">CD-ROM</span> : null}
                </td>
                <td className={bladeCellMuted}>{disk.storage || "—"}</td>
                <td className={`${bladeCellMuted} break-all`}>{disk.volume}</td>
                <td className={`${bladeCell} tabular-nums`}>
                  {disk.sizeBytes > 0 ? formatBytes(disk.sizeBytes, 0) : "—"}
                </td>
                <td className={bladeCell}>
                  {disk.cdrom || disk.sizeBytes === 0 ? null : editing === disk.key ? (
                    <span className="flex items-center gap-2">
                      <Input
                        value={sizeGib}
                        onChange={(e) => {
                          setSizeGib(e.target.value);
                          setErr("");
                        }}
                        aria-label="New size in GiB"
                        invalid={err !== ""}
                        className="w-[90px]"
                      />
                      <span className="text-[12px] text-ink-2">GiB</span>
                      <Button
                        variant="primaryCompact"
                        disabled={resize.isPending}
                        onClick={() => {
                          const v = Number(sizeGib);
                          if (!Number.isInteger(v) || v <= currentGib) {
                            setErr(`Must be an integer above ${currentGib}`);
                            return;
                          }
                          resize.mutate(
                            { disk: disk.key, sizeGib: v },
                            { onSuccess: () => setEditing(null) },
                          );
                        }}
                      >
                        Resize
                      </Button>
                      <Button variant="secondaryCompact" onClick={() => setEditing(null)}>
                        Cancel
                      </Button>
                      {err ? <span className="text-[12px] text-err-text">{err}</span> : null}
                    </span>
                  ) : (
                    <Button
                      variant="link"
                      onClick={() => {
                        setEditing(disk.key);
                        setSizeGib(String(currentGib + 1));
                        setErr("");
                      }}
                    >
                      Resize
                    </Button>
                  )}
                </td>
              </tr>
            );
          })}
        </BladeTable>
      )}
    </div>
  );
}
