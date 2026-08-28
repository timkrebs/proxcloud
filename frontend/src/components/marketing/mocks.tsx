// Decorative portal "screenshots" — faithful React/Tailwind reproductions of
// the design source's heroMock / wizardMock / govMock / catalogMock (originally
// React.createElement trees). Purely decorative chrome → the whole frame is
// aria-hidden; no real data, no portal imports. Token utility classes only.
import type { ReactElement, ReactNode } from "react";

import { MarketingSvc, type MarketingSvcName } from "./icons";

/** Browser-chrome frame with traffic-light dots + a URL/label. */
function Frame({ label, children }: { label: string; children: ReactNode }): ReactElement {
  return (
    <div
      className="min-w-0 overflow-hidden rounded-lg border border-line bg-card shadow-pc-lift"
      aria-hidden
    >
      <div className="flex h-[34px] items-center gap-2 bg-topbar px-3">
        <div className="flex gap-[5px]">
          <span className="h-2 w-2 rounded-full bg-err opacity-80" />
          <span className="h-2 w-2 rounded-full bg-warn opacity-80" />
          <span className="h-2 w-2 rounded-full bg-ok opacity-80" />
        </div>
        <span className="text-[11px] text-topbar-muted">{label}</span>
      </div>
      {children}
    </div>
  );
}

const NAV_ITEMS: { label: string; icon: MarketingSvcName; active?: boolean }[] = [
  { label: "Home", icon: "catalog", active: true },
  { label: "All resources", icon: "catalog" },
  { label: "Virtual machines", icon: "vm" },
  { label: "Kubernetes", icon: "k8s" },
  { label: "Databases", icon: "db" },
  { label: "Networking", icon: "net" },
  { label: "Storage", icon: "store" },
];

const HERO_TILES: [string, string][] = [
  ["vCPU", "32 / 48"],
  ["RAM", "96 / 128 GB"],
  ["Cost", "€412"],
];

const HERO_ROWS: { name: string; type: MarketingSvcName; status: string; dot: string }[] = [
  { name: "web-prod-01", type: "vm", status: "Running", dot: "bg-ok" },
  { name: "apps-prod", type: "k8s", status: "Healthy", dot: "bg-ok" },
  { name: "orders-db", type: "db", status: "Available", dot: "bg-ok" },
  { name: "win-jump-01", type: "vm", status: "Provisioning", dot: "bg-accent" },
];

export function HeroMock(): ReactElement {
  return (
    <Frame label="proxcloud.example/portal">
      <div className="flex h-[268px] bg-canvas">
        <div className="w-[116px] shrink-0 border-r border-line bg-card py-2">
          {NAV_ITEMS.map((item) => (
            <div
              key={item.label}
              className={`flex h-[26px] items-center gap-[7px] px-[9px] text-[9.5px] text-ink ${
                item.active ? "bg-tint2" : "bg-transparent"
              }`}
            >
              <span className="flex w-3">
                <MarketingSvc name={item.icon} size={12} />
              </span>
              {item.label}
            </div>
          ))}
        </div>
        <div className="min-w-0 flex-1 px-[14px] py-3">
          <div className="text-[13px] font-semibold">Good morning, Alex</div>
          <div className="mt-[2px] text-[9.5px] text-ink-2">Tenant aurora-labs · All projects</div>
          <div className="mt-3 flex gap-[7px]">
            {HERO_TILES.map(([t, v]) => (
              <div
                key={t}
                className="min-w-0 flex-1 rounded-[3px] border border-line bg-page px-[9px] py-2"
              >
                <div className="truncate text-[9px] text-ink-2">{t}</div>
                <div className="mt-[2px] text-[14px] font-semibold">{v}</div>
              </div>
            ))}
          </div>
          <div className="mt-3 rounded-[3px] border border-line bg-page">
            <div className="px-[10px] py-2 text-[10px] font-semibold">Recent resources</div>
            {HERO_ROWS.map((row) => (
              <div
                key={row.name}
                className="flex items-center gap-2 border-t border-line px-[10px] py-[7px] text-[10px]"
              >
                <MarketingSvc name={row.type} size={12} />
                <span className="flex-1 text-accent">{row.name}</span>
                <span className="flex items-center gap-[5px] text-ink-2">
                  <span className={`h-[6px] w-[6px] rounded-full ${row.dot}`} />
                  {row.status}
                </span>
              </div>
            ))}
          </div>
        </div>
      </div>
    </Frame>
  );
}

function WizardField({ label, value }: { label: string; value: string }): ReactElement {
  return (
    <div className="mb-2 flex items-center gap-[10px]">
      <span className="basis-[78px] text-[9.5px] text-ink-2">{label}</span>
      <span className="flex h-[22px] flex-1 items-center rounded-[2px] border border-border bg-page px-[7px] text-[9.5px]">
        {value}
      </span>
    </div>
  );
}

function WizardSize({
  name,
  spec,
  on,
}: {
  name: string;
  spec: string;
  on?: boolean;
}): ReactElement {
  return (
    <div
      className={`flex-1 rounded-[2px] px-2 py-[7px] ${
        on ? "border border-accent bg-tint" : "border border-border bg-page"
      }`}
    >
      <div className="text-[11px] font-semibold">{name}</div>
      <div className="mt-[2px] text-[8.5px] text-ink-2">{spec}</div>
    </div>
  );
}

const WIZARD_TABS = ["Basics", "Size", "Disks", "Networking", "Review + create"];

export function WizardMock(): ReactElement {
  return (
    <Frame label="Create a resource">
      <div className="h-[268px] overflow-hidden bg-canvas px-4 py-[14px]">
        <div className="text-[13px] font-semibold">Create a virtual machine</div>
        <div className="mt-[10px] flex gap-[10px] border-b border-line">
          {WIZARD_TABS.map((tab, i) => (
            <span
              key={tab}
              className={`-mb-px border-b-2 pb-[6px] text-[9.5px] ${
                i === 0 ? "border-accent font-semibold text-ink" : "border-transparent text-ink-2"
              }`}
            >
              {tab}
            </span>
          ))}
        </div>
        <div className="mt-3 flex gap-3">
          <div className="min-w-0 flex-1">
            <WizardField label="Project" value="web-prod" />
            <WizardField label="VM name" value="web-prod-02" />
            <WizardField label="Image" value="Ubuntu 24.04 LTS" />
            <div className="mt-[10px] flex gap-[6px]">
              <WizardSize name="S" spec="2 / 4 GB" />
              <WizardSize name="M" spec="4 / 8 GB" on />
              <WizardSize name="L" spec="8 / 16 GB" />
            </div>
          </div>
          <div className="basis-[104px] self-start rounded-[3px] border border-line bg-page p-[9px]">
            <div className="text-[9px] font-semibold">Estimated cost</div>
            <div className="mt-[6px] text-[17px] font-semibold">€44.12</div>
            <div className="mt-[2px] text-[8.5px] text-ink-2">per month</div>
            <div className="mt-2 flex items-center gap-1 text-[8.5px] text-ok">
              <span className="h-[6px] w-[6px] rounded-full bg-ok" />
              Valid
            </div>
          </div>
        </div>
        <div className="mt-[14px] flex gap-[6px] border-t border-line pt-[10px]">
          <span className="rounded-[2px] bg-accent px-3 py-[5px] text-[9.5px] font-semibold text-white">
            Review + create
          </span>
          <span className="rounded-[2px] border border-border px-[10px] py-[5px] text-[9.5px] text-ink-2">
            Next : Size &gt;
          </span>
        </div>
      </div>
    </Frame>
  );
}

function GovBar({
  label,
  usage,
  pct,
}: {
  label: string;
  usage: string;
  pct: number;
}): ReactElement {
  return (
    <div className="mb-[10px]">
      <div className="mb-1 flex justify-between text-[9.5px]">
        <span>{label}</span>
        <span className="text-ink-2">{usage}</span>
      </div>
      <div className="h-1 rounded-[2px] bg-alt">
        <span className={`block h-1 rounded-[2px] bg-accent ${PCT_WIDTH[pct] ?? ""}`} />
      </div>
    </div>
  );
}

// Static width classes for the governance quota bars (Tailwind needs literal
// class names, not runtime-interpolated percentages).
const PCT_WIDTH: Record<number, string> = {
  60: "w-[60%]",
  67: "w-[67%]",
  75: "w-[75%]",
};

function GovRole({
  name,
  role,
  scope,
}: {
  name: string;
  role: string;
  scope: string;
}): ReactElement {
  return (
    <div className="flex items-center gap-2 border-t border-line py-[6px] text-[9.5px]">
      <span className="flex h-[18px] w-[18px] items-center justify-center rounded-full bg-tint2 text-[8px] font-semibold text-accent">
        {name.slice(0, 2).toUpperCase()}
      </span>
      <span className="flex-1">{name}</span>
      <span className="font-semibold">{role}</span>
      <span className="basis-[96px] text-right text-ink-2">{scope}</span>
    </div>
  );
}

export function GovMock(): ReactElement {
  return (
    <Frame label="Access control (IAM)">
      <div className="h-[268px] overflow-hidden bg-canvas px-4 py-[14px]">
        <div className="text-[12px] font-semibold">Tenant aurora-labs</div>
        <div className="mt-[2px] text-[9.5px] text-ink-2">
          3 projects · 12 users · quota 60% used
        </div>
        <div className="mt-[11px] rounded-[3px] border border-line bg-page px-3 py-[11px]">
          <GovBar label="vCPU" usage="32 of 48" pct={67} />
          <GovBar label="RAM" usage="96 of 128 GB" pct={75} />
          <GovBar label="Storage" usage="1.2 of 2 TB" pct={60} />
        </div>
        <div className="mt-[10px] rounded-[3px] border border-line bg-page px-3 py-[9px]">
          <div className="pb-[5px] text-[10px] font-semibold">Role assignments</div>
          <GovRole name="Alex Meyer" role="Owner" scope="Tenant" />
          <GovRole name="ci-bot" role="Contributor" scope="web-prod" />
          <GovRole name="Dana Okafor" role="Reader" scope="web-prod" />
        </div>
      </div>
    </Frame>
  );
}

function CatalogCard({
  type,
  name,
  desc,
}: {
  type: MarketingSvcName;
  name: string;
  desc: string;
}): ReactElement {
  return (
    <div className="rounded-[3px] border border-line bg-page p-[10px]">
      <div className="flex items-center gap-[7px]">
        <MarketingSvc name={type} size={16} />
        <span className="text-[10px] font-semibold">{name}</span>
      </div>
      <div className="mt-[6px] text-[8.5px] leading-[1.5] text-ink-2">{desc}</div>
      <div className="mt-2 border-t border-line pt-[6px] text-[8.5px] text-accent">Create</div>
    </div>
  );
}

export function CatalogMock(): ReactElement {
  return (
    <Frame label="Service catalog">
      <div className="h-[268px] overflow-hidden bg-canvas px-4 py-[14px]">
        <div className="text-[12px] font-semibold">Create a resource</div>
        <div className="mt-[10px] flex h-[22px] max-w-[190px] items-center rounded-[2px] border border-border bg-page px-2 text-[9.5px] text-ink-3">
          Search the catalog
        </div>
        <div className="mt-[11px] grid grid-cols-2 gap-2">
          <CatalogCard
            type="db"
            name="PostgreSQL 16"
            desc="Managed Postgres with automated backups and retention."
          />
          <CatalogCard type="db" name="Redis 7" desc="In-memory cache with optional persistence." />
          <CatalogCard
            type="k8s"
            name="Kubernetes (K3s)"
            desc="Node pools, upgrades, kubeconfig download."
          />
          <CatalogCard
            type="store"
            name="S3 bucket"
            desc="Object storage with access keys and quotas."
          />
        </div>
      </div>
    </Frame>
  );
}
