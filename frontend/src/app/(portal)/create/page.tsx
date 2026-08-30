"use client";
// Catalog — design §3.2 adapted to real offerings: VM and LXC create flows.
import Link from "next/link";
import { useState } from "react";

import { Card } from "@/components/ui/Card";
import { Input } from "@/components/ui/Input";
import { Svc } from "@/components/ui/icons";

const ITEMS = [
  {
    kind: "vm",
    icon: "vm" as const,
    name: "Virtual machine",
    desc: "Linux or other OS from an ISO, or clone an existing template — with cloud-init, snapshots, and live metrics.",
    href: "/create/vm",
  },
  {
    kind: "lxc",
    icon: "lxc" as const,
    name: "LXC container",
    desc: "Lightweight container from a storage template, running in seconds with minimal overhead.",
    href: "/create/lxc",
  },
];

export default function CatalogPage() {
  const [q, setQ] = useState("");
  const items = ITEMS.filter((i) => i.name.toLowerCase().includes(q.toLowerCase()));

  return (
    <div className="max-w-[1360px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">Create a resource</span>
      </nav>
      <h1 className="mb-[18px] text-[24px] font-semibold">Create a resource</h1>

      <Input
        value={q}
        onChange={(e) => setQ(e.target.value)}
        placeholder="Search the catalog"
        aria-label="Search the catalog"
        className="w-[min(100%,420px)]"
      />

      <div className="mt-4 grid grid-cols-[repeat(auto-fill,minmax(250px,1fr))] gap-3">
        {items.map((i) => (
          <Card key={i.kind} hoverable className="flex min-h-[150px] flex-col gap-[10px] p-4">
            <div className="flex items-center gap-2">
              <Svc name={i.icon} size={24} />
              <span className="text-[14px] font-semibold">{i.name}</span>
            </div>
            <p className="flex-1 text-[12px] leading-[1.45] text-ink-2">{i.desc}</p>
            <div className="border-t border-line-row pt-[9px]">
              <Link href={i.href} className="text-[13px]">
                Create
              </Link>
            </div>
          </Card>
        ))}
        {items.length === 0 ? (
          <p className="text-[13px] text-ink-2">Nothing in the catalog matches &quot;{q}&quot;.</p>
        ) : null}
      </div>
    </div>
  );
}
