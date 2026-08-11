"use client";
// Typed-name delete confirmation — design-inventory §3.14 verbatim: red
// callout, consequence list, exact-match gate on the Delete button.
import { useState } from "react";
import { useRouter } from "next/navigation";

import { Button } from "@/components/ui/Button";
import { Flyout } from "@/components/ui/Flyout";
import { Input } from "@/components/ui/Input";
import { Mi } from "@/components/ui/icons";
import type { GuestParams } from "@/lib/api/guestQueries";
import { useDeleteGuest } from "@/lib/api/mutations";

export function DeleteFlyout({
  guest,
  name,
  running,
  onClose,
}: {
  guest: GuestParams;
  name: string;
  running: boolean;
  onClose: () => void;
}) {
  const [text, setText] = useState("");
  const del = useDeleteGuest();
  const router = useRouter();
  const kind = guest.type === "qemu" ? "virtual machine" : "container";
  const match = text === name;

  return (
    <Flyout title={`Delete ${kind}`} onClose={onClose}>
      <div className="mb-4 flex gap-[10px] rounded-fluent border border-err bg-err-bg px-3 py-[10px] text-[13px] leading-[1.5]">
        <Mi name="warn" size={16} color="var(--color-err)" style={{ flexShrink: 0, marginTop: 2 }} />
        <span>
          Deleting <strong>{name}</strong> is permanent and cannot be undone.
        </span>
      </div>

      {running ? (
        <p className="mb-4 text-[13px] text-err-text">
          This {kind} is running — stop it before deleting.
        </p>
      ) : null}

      <div className="mb-[6px] text-[13px] font-semibold">This will also delete</div>
      <ul className="mb-[18px] ml-[18px] list-disc text-[13px] leading-[1.7] text-ink-2">
        <li>All attached disks and their snapshots</li>
        <li>The network interface configuration</li>
        <li>References in backup jobs and the pool</li>
      </ul>

      <div className="mb-[6px] text-[13px]">
        Type <strong>{name}</strong> to confirm
      </div>
      <Input value={text} onChange={(e) => setText(e.target.value)} placeholder={name} aria-label="Confirm name" className="w-full" />

      <div className="mt-4 flex gap-2">
        <Button
          variant="danger"
          disabled={!match || running || del.isPending}
          onClick={() =>
            del.mutate(
              { target: { ...guest, name }, purge: true, confirmName: text },
              {
                onSuccess: () => {
                  onClose();
                  router.push("/resources");
                },
              },
            )
          }
        >
          Delete
        </Button>
        <Button variant="secondary" onClick={onClose}>
          Cancel
        </Button>
      </div>
    </Flyout>
  );
}
