"use client";
// Access gate for the cluster-wide infrastructure pages (Nodes, Storage) —
// platform-admin only in the tenancy model. Tenant users who reach these routes
// directly (the nav rows are hidden for them) get an honest access message,
// never a perpetual skeleton against a 403'd admin endpoint.
import Link from "next/link";

import { EmptyState } from "@/components/ui/EmptyState";

export function AdminOnly({ resource }: { resource: string }) {
  return (
    <div className="max-w-[1360px] px-8 pt-5 pb-10">
      <nav className="mb-[10px] text-[12px]">
        <Link href="/dashboard">Home</Link>
        <span className="text-ink-2"> &gt; </span>
        <span className="text-ink-2">{resource}</span>
      </nav>
      <EmptyState
        variant="page"
        icon="person"
        title="Administrator access required"
        body={`${resource} shows cluster-wide infrastructure and is available to platform administrators only. Your resources live under All resources and Projects.`}
      />
    </div>
  );
}
