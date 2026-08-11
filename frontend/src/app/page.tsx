// Logged-out landing page — design-inventory §3.9, adapted to Proxmox reality
// (VMs + LXC containers; no K8s/DB marketing for products that don't exist).
import Link from "next/link";
import type { ReactNode } from "react";
import { Mi, Svc, BrandLogo } from "@/components/ui/icons";

/** §3.9 service strip cell: 26px icon, 14px/600 name, 12.5px ink-2 description. */
function ServiceCell({
  icon,
  name,
  desc,
}: {
  icon: ReactNode;
  name: string;
  desc: string;
}) {
  return (
    <div className="flex flex-col gap-[9px]">
      {icon}
      <div className="text-[14px] font-semibold">{name}</div>
      <p className="text-[12.5px] leading-[1.5] text-ink-2 text-pretty">{desc}</p>
    </div>
  );
}

/** §3.9 feature trio cell: 20px accent line icon, 15px/600 title, 13px ink-2 body. */
function FeatureCell({
  icon,
  title,
  body,
}: {
  icon: "person" | "chart" | "bolt";
  title: string;
  body: string;
}) {
  return (
    <div>
      <span className="mb-[10px] inline-flex text-accent">
        <Mi name={icon} size={20} color="currentColor" strokeWidth={1.3} />
      </span>
      <h3 className="mb-[6px] text-[15px] font-semibold">{title}</h3>
      <p className="text-[13px] leading-[1.55] text-ink-2 text-pretty">{body}</p>
    </div>
  );
}

export default function LandingPage() {
  return (
    <div className="flex min-h-dvh flex-col bg-card text-ink">
      {/* §3.9 header — 56px dark bar */}
      <header className="flex h-[56px] shrink-0 items-center gap-[10px] bg-topbar px-10">
        <BrandLogo size={22} />
        <span className="text-[16px] font-semibold text-white">Proxcloud</span>
        <div className="ml-auto flex items-center gap-5">
          <Link
            href="/signin"
            className="text-[13px] text-white hover:text-topbar-muted hover:no-underline"
          >
            Sign in
          </Link>
          <Link
            href="/signin"
            className="inline-flex h-8 items-center rounded-fluent bg-accent px-[18px] text-[13px] font-semibold text-white hover:bg-accent-hover hover:text-white hover:no-underline active:bg-accent-active"
          >
            Get started
          </Link>
        </div>
      </header>

      <main className="flex-1">
        {/* §3.9 hero */}
        <section className="mx-auto max-w-[920px] px-10 pt-[88px] pb-[72px] text-center">
          <span className="mb-[22px] inline-flex items-center gap-2 rounded-fluent border border-nav-active bg-selected px-[10px] py-1 text-[12px] font-semibold text-accent">
            Private cloud console for Proxmox VE
          </span>
          <h1 className="text-[44px] font-semibold leading-[1.15] tracking-[-0.5px] text-balance">
            Self-service cloud, on your own metal
          </h1>
          <p className="mx-auto mt-[18px] max-w-[620px] text-[17px] leading-[1.55] text-ink-2 text-pretty">
            Proxcloud turns your Proxmox VE server into a self-service cloud
            portal. Provision virtual machines and LXC containers in minutes —
            with live metrics, honest task progress, and a real console.
          </p>
          <div className="mt-[30px] flex items-center justify-center gap-[10px]">
            <Link
              href="/signin"
              className="inline-flex h-[38px] items-center rounded-fluent bg-accent px-[26px] text-[14px] font-semibold text-white hover:bg-accent-hover hover:text-white hover:no-underline active:bg-accent-active"
            >
              Get started
            </Link>
            <Link
              href="/dashboard"
              className="inline-flex h-[38px] items-center rounded-fluent border border-line-input bg-card px-[22px] text-[14px] text-ink hover:bg-hover hover:text-ink hover:no-underline"
            >
              Explore the console
            </Link>
          </div>
        </section>

        {/* §3.9 service strip — real offerings only */}
        <section className="border-y border-line bg-canvas px-10 py-11">
          <div className="mx-auto grid max-w-[1060px] gap-6 [grid-template-columns:repeat(auto-fit,minmax(180px,1fr))]">
            <ServiceCell
              icon={<Svc name="vm" size={26} />}
              name="Virtual machines"
              desc="Create from ISO or clone a template, with cloud-init, snapshots, and a web console."
            />
            <ServiceCell
              icon={<Svc name="lxc" size={26} />}
              name="LXC containers"
              desc="Lightweight containers from storage templates, running in seconds."
            />
            <ServiceCell
              icon={<Svc name="node" size={26} />}
              name="Nodes"
              desc="Live CPU, memory, and storage metrics for every node."
            />
            <ServiceCell
              icon={<Svc name="vol" size={26} />}
              name="Storage"
              desc="See every pool, its usage, and what's stored where."
            />
            <ServiceCell
              icon={
                <span className="inline-flex text-accent">
                  <Mi name="clock" size={26} color="currentColor" strokeWidth={1.3} />
                </span>
              }
              name="Activity"
              desc="Every operation is a real Proxmox task with live progress."
            />
          </div>
        </section>

        {/* §3.9 feature trio */}
        <section className="mx-auto grid max-w-[1060px] gap-9 px-10 py-14 [grid-template-columns:repeat(auto-fit,minmax(240px,1fr))]">
          <FeatureCell
            icon="person"
            title="Single admin, real auth"
            body="A session-guarded portal; your Proxmox API token never reaches the browser."
          />
          <FeatureCell
            icon="chart"
            title="Live metrics"
            body="Node and guest charts stream over SSE from Proxmox RRD data."
          />
          <FeatureCell
            icon="bolt"
            title="Everything is async"
            body="Provisioning never blocks. Every action is a Proxmox task you can watch complete."
          />
        </section>
      </main>

      {/* §3.9 footer */}
      <footer className="flex items-center justify-between gap-4 border-t border-line px-10 py-5 text-[12px] text-ink-2">
        <span>Proxcloud — runs on your Proxmox VE cluster</span>
        <div className="flex items-center gap-4">
          <a
            href="https://pve.proxmox.com/pve-docs/"
            target="_blank"
            rel="noopener noreferrer"
          >
            Docs
          </a>
        </div>
      </footer>
    </div>
  );
}
