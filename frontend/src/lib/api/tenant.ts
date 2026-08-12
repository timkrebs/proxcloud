"use client";
// Tenancy data layer (Phase 3): the tenant/project scope switch plus project
// CRUD. Reads are Reader; project mutations are Owner (the UI hides the
// controls for non-owners, the backend re-enforces). The active-tenant switch
// persists server-side (sessions.active_tenant_id) then re-derives every scoped
// query — including the SSE ownership filter, which reconnects because
// useEvents depends on the active tenant id.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { ApiError, apiFetch } from "@/lib/api/client";
import type {
  CreateProjectRequest,
  DeleteProjectRequest,
  Member,
  Project,
  RenameProjectRequest,
  SetActiveTenantRequest,
  TenantSummary,
} from "@/lib/api/generated/types";
import { qk } from "@/lib/api/queryKeys";
import { useActiveTenantId, useUiStore } from "@/lib/stores/uiStore";
import { pushToast } from "@/lib/stores/toastStore";

// ── Reads ────────────────────────────────────────────────────────────────────

export function useTenantSummary() {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.tenantSummary(tenantId ?? undefined),
    queryFn: () => apiFetch<TenantSummary>(`/api/tenants/${tenantId}/summary`),
    enabled: tenantId !== null,
  });
}

export function useProjects() {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.projects(tenantId ?? undefined),
    queryFn: () => apiFetch<Project[]>(`/api/tenants/${tenantId}/projects`),
    enabled: tenantId !== null,
    staleTime: 30_000,
  });
}

/** Tenant members — Owner-only read (read-only in Phase 3). */
export function useMembers() {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.members(tenantId ?? undefined),
    queryFn: () => apiFetch<Member[]>(`/api/tenants/${tenantId}/members`),
    enabled: tenantId !== null,
  });
}

// ── Tenant switch ────────────────────────────────────────────────────────────

export function useSwitchTenant() {
  const qc = useQueryClient();
  const setActiveTenant = useUiStore((s) => s.setActiveTenant);
  return useMutation({
    mutationFn: (tenantId: string) =>
      apiFetch<void>("/api/auth/active-tenant", {
        method: "PATCH",
        body: JSON.stringify({ tenantId } satisfies SetActiveTenantRequest),
      }),
    onSuccess: (_res, tenantId) => {
      // Persist the choice, drop the stale project filter, then refetch every
      // scoped query under the new tenant. The SSE stream reconnects on its own
      // (useEvents' effect depends on the active tenant id).
      setActiveTenant(tenantId);
      // Refetch tenant-scoped data under the new tenant; leave tenant-agnostic
      // queries (me, sessions, pricing, notifications) alone.
      const tenantAgnostic = new Set(["me", "sessions", "pricing", "notifications"]);
      qc.invalidateQueries({ predicate: (q) => !tenantAgnostic.has(String(q.queryKey[0])) });
    },
    onError: (err) =>
      pushToast({
        kind: "err",
        title: "Could not switch directory",
        desc: err instanceof ApiError ? err.detail : "Request failed",
      }),
  });
}

// ── Project CRUD (Owner) ─────────────────────────────────────────────────────

function invalidateProjects(qc: ReturnType<typeof useQueryClient>, tenantId: string | null) {
  qc.invalidateQueries({ queryKey: qk.projects(tenantId ?? undefined) });
  qc.invalidateQueries({ queryKey: qk.tenantSummary(tenantId ?? undefined) });
}

export function useCreateProject() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: (name: string) =>
      apiFetch<Project>(`/api/tenants/${tenantId}/projects`, {
        method: "POST",
        body: JSON.stringify({ name } satisfies CreateProjectRequest),
      }),
    onSuccess: (p) => {
      invalidateProjects(qc, tenantId);
      pushToast({ kind: "ok", title: "Project created", desc: p.name });
    },
  });
}

export function useRenameProject() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: ({ id, name }: { id: string; name: string }) =>
      apiFetch<Project>(`/api/tenants/${tenantId}/projects/${encodeURIComponent(id)}`, {
        method: "PATCH",
        body: JSON.stringify({ name } satisfies RenameProjectRequest),
      }),
    onSuccess: (p) => {
      invalidateProjects(qc, tenantId);
      pushToast({ kind: "ok", title: "Project renamed", desc: p.name });
    },
  });
}

export function useDeleteProject() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: ({ id, confirmName }: { id: string; confirmName: string }) =>
      apiFetch<void>(`/api/tenants/${tenantId}/projects/${encodeURIComponent(id)}`, {
        method: "DELETE",
        body: JSON.stringify({ confirmName } satisfies DeleteProjectRequest),
      }),
    onSuccess: () => invalidateProjects(qc, tenantId),
  });
}
