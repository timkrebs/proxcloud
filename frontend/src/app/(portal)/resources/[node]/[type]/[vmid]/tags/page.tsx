"use client";
// Tags blade — design §3.5.4 adapted to PVE reality: flat lowercase labels
// (not key:value), edited via PATCH /config.
import { useState } from "react";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { BladeHeading, useGuestParams } from "@/components/guest/common";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { Mi } from "@/components/ui/icons";
import { useGuest, useUpdateGuestConfig } from "@/lib/api/guestQueries";

const TAG_RE = /^[a-z0-9_][a-z0-9_.-]*$/;

export default function GuestTagsPage() {
  const g = useGuestParams();
  const guest = useGuest(g);
  const update = useUpdateGuestConfig(g);
  const [draft, setDraft] = useState("");
  const [err, setErr] = useState("");

  if (guest.isPending) return <Skeleton className="h-32" />;
  if (guest.isError) return <CardError err={guest.error} />;
  const tags = guest.data.tags;

  const save = (next: string[]) => update.mutate({ tags: next });

  return (
    <div>
      <BladeHeading>Tags</BladeHeading>
      <p className="mb-4 -mt-1 text-[12px] text-ink-2">
        Proxmox tags are flat labels (lowercase letters, digits, <code>. - _</code>) — not key:value
        pairs.
      </p>

      <div className="mb-4 flex flex-wrap gap-2">
        {tags.length === 0 ? <span className="text-[13px] text-ink-2">No tags yet.</span> : null}
        {tags.map((t) => (
          <span
            key={t}
            className="flex items-center gap-2 rounded-fluent border border-line bg-hover px-[10px] py-[5px] text-[13px]"
          >
            {t}
            <button
              type="button"
              title="Remove tag"
              className="cursor-pointer border-none bg-transparent p-0 text-ink-2 hover:text-err"
              onClick={() => save(tags.filter((x) => x !== t))}
            >
              <Mi name="close" size={11} color="currentColor" />
            </button>
          </span>
        ))}
      </div>

      <div className="flex items-start gap-2">
        <div>
          <Input
            value={draft}
            onChange={(e) => {
              setDraft(e.target.value);
              setErr("");
            }}
            placeholder="e.g. env-prod"
            aria-label="New tag"
            invalid={err !== ""}
            className="w-[220px]"
          />
          {err ? <p className="mt-1 text-[12px] text-err-text">{err}</p> : null}
        </div>
        <Button
          variant="secondaryCompact"
          disabled={update.isPending}
          onClick={() => {
            const t = draft.trim();
            if (!TAG_RE.test(t)) {
              setErr("Lowercase letters, digits, . - _ only; must not start with . or -");
              return;
            }
            if (tags.includes(t)) {
              setErr("Tag already present");
              return;
            }
            save([...tags, t]);
            setDraft("");
          }}
        >
          Add
        </Button>
      </div>
    </div>
  );
}
