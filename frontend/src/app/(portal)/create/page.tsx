"use client";
// Create a resource — design §3.2 marketplace gallery. Two kinds of tile:
//  1. Compute entry tiles (VM / LXC) — always available, route into the wizard.
//  2. Service-catalog tiles (ADR-0026) — fetched live from GET /service-catalog,
//     grouped by category, routing into the same wizard prefilled from the
//     service def (?service=<id>). The catalog is feature-flagged on the
//     backend: when off the endpoint 404s and the catalog section is hidden.
import Link from "next/link";
import { useMemo, useState } from "react";

import { CardError, Skeleton } from "@/components/dashboard/DashboardCards";
import { Card } from "@/components/ui/Card";
import { Input } from "@/components/ui/Input";
import { Svc, type SvcName } from "@/components/ui/icons";
import type { CatalogService } from "@/lib/api/generated/types";
import { isCatalogDisabled, useServiceCatalog } from "@/lib/api/serviceCatalog";

interface EntryTile {
  key: string;
  icon: SvcName;
  name: string;
  desc: string;
  href: string;
  category: string;
}

const ENTRY_TILES: EntryTile[] = [
  {
    key: "vm",
    icon: "vm",
    name: "Virtual machine",
    desc: "Linux or other OS from an ISO, or clone an existing template — with cloud-init, snapshots, and live metrics.",
    href: "/create/vm",
    category: "Compute",
  },
  {
    key: "lxc",
    icon: "lxc",
    name: "LXC container",
    desc: "Lightweight container from a storage template, running in seconds with minimal overhead.",
    href: "/create/lxc",
    category: "Compute",
  },
];

// The Svc glyph set has no per-product database icons, so map the catalog's
// icon/id hint onto the closest available product glyph (a stacked-volume icon
// for data services, the k8s glyph for cluster sets).
const SVC_ICON: Record<string, SvcName> = { k8s: "k8s", k3s: "k8s", kubernetes: "k8s" };

function serviceIcon(svc: CatalogService): SvcName {
  return SVC_ICON[svc.icon] ?? SVC_ICON[svc.id] ?? (svc.kind === "set" ? "k8s" : "vol");
}

function serviceHref(svc: CatalogService): string {
  const kind = svc.guestType === "lxc" ? "lxc" : "vm";
  return `/create/${kind}?service=${encodeURIComponent(svc.id)}`;
}

function matches(q: string, ...fields: string[]): boolean {
  if (q === "") return true;
  const needle = q.toLowerCase();
  return fields.some((f) => f.toLowerCase().includes(needle));
}

function Tile({
  icon,
  name,
  desc,
  href,
}: {
  icon: SvcName;
  name: string;
  desc: string;
  href: string;
}) {
  return (
    <Card hoverable className="flex min-h-[150px] flex-col gap-[10px] p-4">
      <div className="flex items-center gap-2">
        <Svc name={icon} size={24} />
        <span className="text-[14px] font-semibold">{name}</span>
      </div>
      <p className="flex-1 text-[12px] leading-[1.45] text-ink-2">{desc}</p>
      <div className="border-t border-line-row pt-[9px]">
        <Link href={href} className="text-[13px]">
          Create
        </Link>
      </div>
    </Card>
  );
}

function TileGrid({ children }: { children: React.ReactNode }) {
  return (
    <div className="mt-3 grid grid-cols-[repeat(auto-fill,minmax(250px,1fr))] gap-3">
      {children}
    </div>
  );
}

function CategoryGroup({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="mt-6">
      <h2 className="text-[14px] font-semibold">{title}</h2>
      <TileGrid>{children}</TileGrid>
    </section>
  );
}

export default function CatalogPage() {
  const [q, setQ] = useState("");
  const catalog = useServiceCatalog();

  const entryTiles = ENTRY_TILES.filter((t) => matches(q, t.name, t.desc));

  // Group the (filtered) catalog services by category, preserving API order.
  const serviceGroups = useMemo(() => {
    const groups = new Map<string, CatalogService[]>();
    for (const svc of catalog.data ?? []) {
      if (!matches(q, svc.displayName, svc.description, svc.category)) continue;
      const list = groups.get(svc.category) ?? [];
      list.push(svc);
      groups.set(svc.category, list);
    }
    return [...groups.entries()];
  }, [catalog.data, q]);

  const serviceMatches = serviceGroups.reduce((n, [, list]) => n + list.length, 0);
  const catalogFailed = catalog.isError && !isCatalogDisabled(catalog.error);
  const noMatches = !catalog.isLoading && entryTiles.length === 0 && serviceMatches === 0;

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
        placeholder="Search compute and services"
        aria-label="Search the catalog"
        className="w-[min(100%,420px)]"
      />

      {/* Compute entry tiles — always available (not backend-gated). */}
      {entryTiles.length > 0 ? (
        <CategoryGroup title="Compute">
          {entryTiles.map((t) => (
            <Tile key={t.key} icon={t.icon} name={t.name} desc={t.desc} href={t.href} />
          ))}
        </CategoryGroup>
      ) : null}

      {/* Service catalog — loading / error / (disabled or empty → hidden). */}
      {catalog.isLoading ? (
        <section className="mt-6">
          <Skeleton className="h-5 w-40" />
          <TileGrid>
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-[150px]" />
            ))}
          </TileGrid>
        </section>
      ) : catalogFailed ? (
        <section className="mt-6">
          <h2 className="mb-2 text-[14px] font-semibold">Service catalog</h2>
          <CardError err={catalog.error} />
        </section>
      ) : (
        serviceGroups.map(([category, list]) => (
          <CategoryGroup key={category} title={category}>
            {list.map((svc) => (
              <Tile
                key={svc.id}
                icon={serviceIcon(svc)}
                name={svc.displayName}
                desc={svc.description}
                href={serviceHref(svc)}
              />
            ))}
          </CategoryGroup>
        ))
      )}

      {noMatches ? (
        <p className="mt-6 text-[13px] text-ink-2">
          Nothing in the catalog matches &quot;{q}&quot;.
        </p>
      ) : null}
    </div>
  );
}
