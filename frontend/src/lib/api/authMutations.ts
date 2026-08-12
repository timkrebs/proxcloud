"use client";
// Self-service auth data hooks (Phase 2, local auth). Sessions list plus the
// change-password and revoke-session mutations. All of these live behind the
// portal's <Providers>, so TanStack Query is available; the sign-in and
// bootstrap cards deliberately use plain apiFetch instead (no query client in
// the (auth) route group).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api/client";
import { qk } from "@/lib/api/queryKeys";
import type { ChangePasswordRequest, SessionInfo } from "@/lib/api/generated/types";

/** GET /api/auth/sessions — the caller's own live server-side sessions. */
export function useSessions() {
  return useQuery({
    queryKey: qk.sessions,
    queryFn: () => apiFetch<SessionInfo[]>("/api/auth/sessions"),
    staleTime: 30_000,
  });
}

/**
 * POST /api/auth/password — changes the password and (server-side) revokes all
 * OTHER sessions. On success we invalidate both the sessions list and me so the
 * UI reflects the newly single-session state.
 */
export function useChangePassword() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: ChangePasswordRequest) =>
      apiFetch<void>("/api/auth/password", { method: "POST", body: JSON.stringify(body) }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.sessions });
      qc.invalidateQueries({ queryKey: qk.me });
    },
  });
}

/** DELETE /api/auth/sessions/{id} — revoke one other session. */
export function useRevokeSession() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (id: string) =>
      apiFetch<void>(`/api/auth/sessions/${encodeURIComponent(id)}`, { method: "DELETE" }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: qk.sessions });
    },
  });
}
