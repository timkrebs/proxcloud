"use client";
// Phase 5 account-security + invitations data layer (portal). Everything here
// lives behind the portal's <Providers>, so TanStack Query is available. The
// sign-in TOTP step and the public invite-accept screen deliberately do NOT use
// these hooks — they run in the (auth) route group which has no query client and
// call plain apiFetch instead.
//
// Two families:
//  - Account TOTP (enroll / verify-enroll / disable / regenerate) — self-service,
//    tenant-agnostic. Enroll/disable/regenerate change Me (totpEnabled,
//    recoveryCodesRemaining) so they invalidate qk.me.
//  - Tenant invitations (list / create / revoke) — Owner-only, tenant-scoped.
//    They prepend /api/tenants/${activeTenantId} and wait for an active tenant.
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";

import { apiFetch } from "@/lib/api/client";
import type {
  CreateInvitationRequest,
  EnrollTOTPResponse,
  Invitation,
  PasswordConfirmRequest,
  RecoveryCodesResponse,
  VerifyEnrollRequest,
  VerifyEnrollResponse,
} from "@/lib/api/generated/types";
import { qk } from "@/lib/api/queryKeys";
import { useActiveTenantId } from "@/lib/stores/uiStore";

// ── Account TOTP (self-service) ──────────────────────────────────────────────

/** POST /api/auth/totp/enroll — mints a pending secret + QR. 409 if already on. */
export function useEnrollTotp() {
  return useMutation({
    mutationFn: () =>
      apiFetch<EnrollTOTPResponse>("/api/auth/totp/enroll", { method: "POST", body: "{}" }),
  });
}

/**
 * POST /api/auth/totp/verify — confirms the pending secret with a 6-digit code
 * and, on success, returns the ten recovery codes ONCE. Turning TOTP on flips
 * Me.totpEnabled and seeds recoveryCodesRemaining, so invalidate qk.me.
 */
export function useVerifyEnrollTotp() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: VerifyEnrollRequest) =>
      apiFetch<VerifyEnrollResponse>("/api/auth/totp/verify", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.me }),
  });
}

/** POST /api/auth/totp/disable — password re-prompt; clears TOTP + codes. */
export function useDisableTotp() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: PasswordConfirmRequest) =>
      apiFetch<void>("/api/auth/totp/disable", { method: "POST", body: JSON.stringify(body) }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.me }),
  });
}

/**
 * POST /api/auth/totp/recovery-codes — password re-prompt; replaces the code set
 * and returns the new ten ONCE. Resets recoveryCodesRemaining, so invalidate qk.me.
 */
export function useRegenerateRecoveryCodes() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: PasswordConfirmRequest) =>
      apiFetch<RecoveryCodesResponse>("/api/auth/totp/recovery-codes", {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => qc.invalidateQueries({ queryKey: qk.me }),
  });
}

// ── Tenant invitations (Owner) ───────────────────────────────────────────────

/** GET /api/tenants/{tenantId}/invitations — pending invites (Owner-only). */
export function useInvitations(enabled = true) {
  const tenantId = useActiveTenantId();
  return useQuery({
    queryKey: qk.invitations(tenantId ?? undefined),
    queryFn: () => apiFetch<Invitation[]>(`/api/tenants/${tenantId}/invitations`),
    // Owner-gated route: don't fire (and 403) for non-Owners.
    enabled: tenantId !== null && enabled,
  });
}

function invalidateInvites(qc: ReturnType<typeof useQueryClient>, tenantId: string | null) {
  qc.invalidateQueries({ queryKey: qk.invitations(tenantId ?? undefined) });
  qc.invalidateQueries({ queryKey: qk.members(tenantId ?? undefined) });
}

/** POST /api/tenants/{tenantId}/invitations — 201 Invitation (no token). */
export function useCreateInvitation() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: (body: CreateInvitationRequest) =>
      apiFetch<Invitation>(`/api/tenants/${tenantId}/invitations`, {
        method: "POST",
        body: JSON.stringify(body),
      }),
    onSuccess: () => invalidateInvites(qc, tenantId),
  });
}

/** DELETE /api/tenants/{tenantId}/invitations/{id} — revoke a pending invite. */
export function useRevokeInvitation() {
  const qc = useQueryClient();
  const tenantId = useActiveTenantId();
  return useMutation({
    mutationFn: (invitationId: string) =>
      apiFetch<void>(`/api/tenants/${tenantId}/invitations/${encodeURIComponent(invitationId)}`, {
        method: "DELETE",
      }),
    onSuccess: () => invalidateInvites(qc, tenantId),
  });
}
