"use client";
// Size blade — design §3.5.8: T-shirt preset cards prefill cores/RAM, plus
// exact fields, description, and start-on-boot. Changes go through PATCH
// /config (real task for qemu; memory changes may need a restart to apply).
import { useEffect, useState } from "react";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { BladeHeading, useGuestParams } from "@/components/guest/common";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { Textarea } from "@/components/ui/Textarea";
import { Toggle } from "@/components/ui/Toggle";
import { Mi } from "@/components/ui/icons";
import { useGuest, useUpdateGuestConfig } from "@/lib/api/guestQueries";

const PRESETS = [
  { name: "S", cores: 2, ramGiB: 4 },
  { name: "M", cores: 4, ramGiB: 8 },
  { name: "L", cores: 8, ramGiB: 16 },
  { name: "XL", cores: 16, ramGiB: 32 },
];

export default function GuestSizePage() {
  const g = useGuestParams();
  const guest = useGuest(g);
  const update = useUpdateGuestConfig(g);

  const [cores, setCores] = useState("");
  const [ramMiB, setRamMiB] = useState("");
  const [desc, setDesc] = useState("");
  const [onBoot, setOnBoot] = useState(false);
  const [err, setErr] = useState("");

  useEffect(() => {
    if (guest.data) {
      setCores(String(guest.data.cores));
      setRamMiB(String(Math.round(guest.data.memMax / 2 ** 20)));
      setDesc(guest.data.description ?? "");
      setOnBoot(guest.data.onBoot);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [guest.data?.id]);

  if (guest.isPending) return <Skeleton className="h-40" />;
  if (guest.isError) return <CardError err={guest.error} />;
  const d = guest.data;

  const activePreset = PRESETS.find(
    (p) => String(p.cores) === cores && String(p.ramGiB * 1024) === ramMiB,
  );
  const dirty =
    cores !== String(d.cores) ||
    ramMiB !== String(Math.round(d.memMax / 2 ** 20)) ||
    desc !== (d.description ?? "") ||
    onBoot !== d.onBoot;

  const save = () => {
    const c = Number(cores);
    const m = Number(ramMiB);
    if (!Number.isInteger(c) || c < 1 || c > 128) {
      setErr("Cores must be an integer between 1 and 128.");
      return;
    }
    if (!Number.isInteger(m) || m < 128) {
      setErr("Memory must be at least 128 MiB.");
      return;
    }
    setErr("");
    update.mutate({
      ...(c !== d.cores ? { cores: c } : {}),
      ...(m !== Math.round(d.memMax / 2 ** 20) ? { memoryMb: m } : {}),
      ...(desc !== (d.description ?? "") ? { description: desc } : {}),
      ...(onBoot !== d.onBoot ? { onBoot } : {}),
    });
  };

  return (
    <div className="max-w-[720px]">
      <BladeHeading>Size</BladeHeading>
      <p className="mb-[14px] -mt-1 text-[12px] text-ink-2">
        {d.type === "qemu"
          ? "Core changes apply live; memory changes take effect after a restart."
          : "Container resources apply live."}
      </p>

      <div className="mb-5 grid grid-cols-[repeat(auto-fill,minmax(160px,1fr))] gap-[10px]">
        {PRESETS.map((p) => {
          const selected = activePreset?.name === p.name;
          return (
            <button
              key={p.name}
              type="button"
              onClick={() => {
                setCores(String(p.cores));
                setRamMiB(String(p.ramGiB * 1024));
                setErr("");
              }}
              className={`cursor-pointer rounded-fluent p-[14px] text-left ${
                selected
                  ? "border border-accent bg-selected shadow-[inset_0_0_0_1px_var(--color-accent)]"
                  : "border border-line-soft bg-card hover:border-accent"
              }`}
            >
              <div className="flex items-start justify-between">
                <span className="text-[16px] font-semibold">{p.name}</span>
                {selected ? <Mi name="checkC" size={16} color="var(--color-accent)" /> : null}
              </div>
              <div className="text-[13px]">
                {p.cores} vCPU · {p.ramGiB} GiB RAM
              </div>
            </button>
          );
        })}
      </div>

      <div className="mb-[14px] flex items-center">
        <label className="w-[220px] flex-none text-[14px]">Cores</label>
        <Input
          value={cores}
          onChange={(e) => setCores(e.target.value)}
          className="w-[120px]"
          aria-label="Cores"
        />
      </div>
      <div className="mb-[14px] flex items-center">
        <label className="w-[220px] flex-none text-[14px]">Memory (MiB)</label>
        <Input
          value={ramMiB}
          onChange={(e) => setRamMiB(e.target.value)}
          className="w-[120px]"
          aria-label="Memory MiB"
        />
      </div>
      <div className="mb-[14px] flex items-start">
        <label className="w-[220px] flex-none pt-1 text-[14px]">Description</label>
        <Textarea
          value={desc}
          onChange={(e) => setDesc(e.target.value)}
          rows={4}
          className="w-[300px]"
          aria-label="Description"
        />
      </div>
      <div className="mb-[18px] flex items-center">
        <label className="w-[220px] flex-none text-[14px]">Start on boot</label>
        <Toggle checked={onBoot} onChange={setOnBoot} aria-label="Start on boot" />
      </div>

      {err ? <p className="mb-3 text-[12px] text-err-text">{err}</p> : null}
      <Button variant="primary" disabled={!dirty || update.isPending} onClick={save}>
        Apply changes
      </Button>
    </div>
  );
}
