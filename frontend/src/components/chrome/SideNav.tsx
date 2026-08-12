"use client";

// Left navigation rail — 220px expanded / 48px collapsed (design-inventory §2.4).
// Every item: 36px tall, exact 48px icon slot (so icons stay centered when the
// rail collapses and labels clip away), 13px label, hover #F3F2F1, active
// #DEECF9, native title tooltip for the collapsed state.
//
// Phase 3: cluster-wide infrastructure (Nodes, Storage) is platform-admin only
// — those rows are hidden for tenant users, who see the tenant-scoped surface
// (All resources, VMs, LXC, Projects) instead.
import { Suspense, type ReactNode } from "react";
import Link from "next/link";
import { usePathname, useSearchParams } from "next/navigation";
import { Mi, Svc } from "@/components/ui/icons";
import { useMe } from "@/lib/api/queries";
import { useUiStore } from "@/lib/stores/uiStore";

interface NavItem {
  label: string;
  href: string;
  icon: ReactNode;
  /** Active-state test against the current pathname and ?type= param. */
  match: (pathname: string, type: string | null) => boolean;
  /** Hidden for non-platform-admins (cluster-wide infrastructure). */
  adminOnly?: boolean;
}

type Row =
  | { kind: "item"; item: NavItem }
  | { kind: "divider" }
  | { kind: "label"; text: string };

const prefix =
  (base: string) =>
  (pathname: string): boolean =>
    pathname === base || pathname.startsWith(base + "/");

const item = (
  label: string,
  href: string,
  icon: ReactNode,
  match: NavItem["match"],
  adminOnly = false,
): Row => ({ kind: "item", item: { label, href, icon, match, adminOnly } });

const ROWS: Row[] = [
  item(
    "Create a resource",
    "/create",
    <Mi name="plus" size={16} color="var(--color-accent)" strokeWidth={1.5} />,
    (p) => prefix("/create")(p),
  ),
  { kind: "divider" },
  item(
    "Home",
    "/dashboard",
    <Mi name="home" size={16} color="var(--color-accent)" strokeWidth={1.4} />,
    (p) => prefix("/dashboard")(p),
  ),
  item(
    "All resources",
    "/resources",
    <Svc name="allres" size={16} />,
    (p, t) => p === "/resources" && !t,
  ),
  item(
    "Projects",
    "/projects",
    <Mi name="grid" size={16} color="var(--color-accent)" strokeWidth={1.4} />,
    (p) => prefix("/projects")(p),
  ),
  { kind: "divider" },
  { kind: "label", text: "Favorites" },
  item(
    "Virtual machines",
    "/resources?type=qemu",
    <Svc name="vm" size={17} />,
    (p, t) => p === "/resources" && t === "qemu",
  ),
  item(
    "LXC containers",
    "/resources?type=lxc",
    <Svc name="lxc" size={17} />,
    (p, t) => p === "/resources" && t === "lxc",
  ),
  item("Nodes", "/nodes", <Svc name="node" size={17} />, (p) => prefix("/nodes")(p), true),
  item("Storage", "/storage", <Svc name="vol" size={17} />, (p) => prefix("/storage")(p), true),
  { kind: "divider" },
  item(
    "Activity log",
    "/activity",
    <Mi name="clock" size={16} color="var(--color-ink-2)" />,
    (p) => prefix("/activity")(p),
  ),
  item(
    "Settings",
    "/settings",
    <Mi name="gear" size={16} color="var(--color-ink-2)" />,
    (p) => prefix("/settings")(p),
  ),
];

function Rows({
  collapsed,
  pathname,
  type,
  isAdmin,
}: {
  collapsed: boolean;
  pathname: string;
  type: string | null;
  isAdmin: boolean;
}) {
  return (
    <>
      {ROWS.map((row, i) => {
        if (row.kind === "divider") {
          return <div key={i} className="mx-3 my-1.5 h-px bg-line" />;
        }
        if (row.kind === "label") {
          // Section label — hidden entirely when collapsed (§2.4.5)
          return collapsed ? null : (
            <div key={i} className="pt-0.5 pb-1 pl-[14px] text-[11px] text-ink-2">
              {row.text}
            </div>
          );
        }
        const { label, href, icon, match, adminOnly } = row.item;
        if (adminOnly && !isAdmin) return null;
        const active = match(pathname, type);
        return (
          <Link
            key={i}
            href={href}
            title={label}
            aria-current={active ? "page" : undefined}
            className={`flex h-9 w-full items-center text-[13px] whitespace-nowrap text-ink hover:text-ink hover:no-underline ${
              active ? "bg-nav-active" : "hover:bg-hover"
            }`}
          >
            <span className="flex w-12 flex-none items-center justify-center">{icon}</span>
            <span className="truncate pr-2">{label}</span>
          </Link>
        );
      })}
    </>
  );
}

// useSearchParams requires a Suspense boundary during prerendering, so the
// param-aware list lives behind one; the fallback renders the same rows
// without a type filter (only /resources?type= items lose their highlight).
function RowsWithParams({ collapsed, isAdmin }: { collapsed: boolean; isAdmin: boolean }) {
  const pathname = usePathname();
  const type = useSearchParams().get("type");
  return <Rows collapsed={collapsed} pathname={pathname} type={type} isAdmin={isAdmin} />;
}

function RowsFallback({ collapsed, isAdmin }: { collapsed: boolean; isAdmin: boolean }) {
  const pathname = usePathname();
  return <Rows collapsed={collapsed} pathname={pathname} type={null} isAdmin={isAdmin} />;
}

export default function SideNav() {
  const collapsed = useUiStore((s) => s.navCollapsed);
  const isAdmin = !!useMe().data?.isPlatformAdmin;
  return (
    <nav
      aria-label="Primary"
      className={`flex-none overflow-x-hidden overflow-y-auto border-r border-line bg-card py-2 transition-[width] duration-150 ease-[ease] ${
        collapsed ? "w-12" : "w-[220px]"
      }`}
    >
      <Suspense fallback={<RowsFallback collapsed={collapsed} isAdmin={isAdmin} />}>
        <RowsWithParams collapsed={collapsed} isAdmin={isAdmin} />
      </Suspense>
    </nav>
  );
}
