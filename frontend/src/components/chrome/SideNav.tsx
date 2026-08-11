"use client";

// Left navigation rail — 220px expanded / 48px collapsed (design-inventory §2.4).
// Every item: 36px tall, exact 48px icon slot (so icons stay centered when the
// rail collapses and labels clip away), 13px label, hover #F3F2F1, active
// #DEECF9, native title tooltip for the collapsed state.
import { Suspense, type ReactNode } from "react";
import Link from "next/link";
import { usePathname, useSearchParams } from "next/navigation";
import { Mi, Svc } from "@/components/ui/icons";
import { useUiStore } from "@/lib/stores/uiStore";

interface NavItem {
  label: string;
  href: string;
  icon: ReactNode;
  /** Active-state test against the current pathname and ?type= param. */
  match: (pathname: string, type: string | null) => boolean;
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
): Row => ({ kind: "item", item: { label, href, icon, match } });

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
  item("Nodes", "/nodes", <Svc name="node" size={17} />, (p) => prefix("/nodes")(p)),
  item("Storage", "/storage", <Svc name="vol" size={17} />, (p) => prefix("/storage")(p)),
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
}: {
  collapsed: boolean;
  pathname: string;
  type: string | null;
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
        const { label, href, icon, match } = row.item;
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
function RowsWithParams({ collapsed }: { collapsed: boolean }) {
  const pathname = usePathname();
  const type = useSearchParams().get("type");
  return <Rows collapsed={collapsed} pathname={pathname} type={type} />;
}

function RowsFallback({ collapsed }: { collapsed: boolean }) {
  const pathname = usePathname();
  return <Rows collapsed={collapsed} pathname={pathname} type={null} />;
}

export default function SideNav() {
  const collapsed = useUiStore((s) => s.navCollapsed);
  return (
    <nav
      aria-label="Primary"
      className={`flex-none overflow-x-hidden overflow-y-auto border-r border-line bg-card py-2 transition-[width] duration-150 ease-[ease] ${
        collapsed ? "w-12" : "w-[220px]"
      }`}
    >
      <Suspense fallback={<RowsFallback collapsed={collapsed} />}>
        <RowsWithParams collapsed={collapsed} />
      </Suspense>
    </nav>
  );
}
