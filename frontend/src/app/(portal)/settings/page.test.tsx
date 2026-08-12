// Component test for the Settings page's three-state requirement (loading /
// empty / error) on the Active-sessions list, plus the change-password form's
// client-side guards. The data hooks are mocked so each render branch is
// exercised deterministically — no network, no timers, no flakiness.
import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ApiError } from "@/lib/api/client";

// next/link needs no router in a unit render — reduce it to a plain anchor.
vi.mock("next/link", () => ({
  default: ({ children, href }: { children: React.ReactNode; href: unknown }) => (
    <a href={typeof href === "string" ? href : "#"}>{children}</a>
  ),
}));
vi.mock("@/lib/stores/toastStore", () => ({ pushToast: vi.fn() }));
vi.mock("@/lib/api/queries", () => ({ useMe: vi.fn() }));
vi.mock("@/lib/api/authMutations", () => ({
  useSessions: vi.fn(),
  useChangePassword: vi.fn(),
  useRevokeSession: vi.fn(),
}));

import SettingsPage from "./page";
import { useMe } from "@/lib/api/queries";
import { useChangePassword, useRevokeSession, useSessions } from "@/lib/api/authMutations";

const mockUseMe = vi.mocked(useMe);
const mockUseSessions = vi.mocked(useSessions);
const mockUseChangePassword = vi.mocked(useChangePassword);
const mockUseRevokeSession = vi.mocked(useRevokeSession);

// Minimal query/mutation shapes the settings page reads. Cast through unknown so
// the test does not need the full TanStack Query result type surface.
function query(overrides: Record<string, unknown>) {
  return { isPending: false, isError: false, error: null, data: undefined, refetch: vi.fn(), ...overrides } as never;
}
function mutation(overrides: Record<string, unknown> = {}) {
  return { mutate: vi.fn(), isPending: false, ...overrides } as never;
}

// vitest.config has no `globals`, so @testing-library/react's automatic
// per-test cleanup is not registered — do it explicitly or renders accumulate.
afterEach(() => cleanup());

beforeEach(() => {
  vi.clearAllMocks();
  // Account section resolves so only the sessions branch varies per test.
  mockUseMe.mockReturnValue(
    query({
      data: {
        id: "u1",
        email: "founder@proxcloud.local",
        displayName: "Founder",
        isPlatformAdmin: true,
        totpEnabled: false,
      },
    }),
  );
  mockUseChangePassword.mockReturnValue(mutation());
  mockUseRevokeSession.mockReturnValue(mutation());
});

describe("Settings active-sessions — three states", () => {
  it("loading: shows skeletons, no empty/error/table", () => {
    mockUseSessions.mockReturnValue(query({ isPending: true }));
    const { container } = render(<SettingsPage />);

    expect(container.querySelectorAll(".animate-pulse").length).toBeGreaterThan(0);
    expect(screen.queryByText("No active sessions.")).toBeNull();
    expect(screen.queryByRole("table")).toBeNull();
    expect(screen.queryByRole("button", { name: "Retry" })).toBeNull();
  });

  it("error: shows the real backend message and a Retry that refetches", () => {
    const refetch = vi.fn();
    mockUseSessions.mockReturnValue(
      query({
        isError: true,
        error: new ApiError(500, { code: "internal", message: "sessions store unavailable" }),
        refetch,
      }),
    );
    render(<SettingsPage />);

    expect(screen.getByText("sessions store unavailable")).toBeTruthy();
    const retry = screen.getByRole("button", { name: "Retry" });
    fireEvent.click(retry);
    expect(refetch).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("empty: shows the empty state, no table", () => {
    mockUseSessions.mockReturnValue(query({ data: [] }));
    render(<SettingsPage />);

    expect(screen.getByText("No active sessions.")).toBeTruthy();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("populated: renders a row per session, marks the current one, offers Revoke on others", () => {
    mockUseSessions.mockReturnValue(
      query({
        data: [
          {
            id: "s-current",
            createdAt: new Date().toISOString(),
            lastSeenAt: new Date().toISOString(),
            ip: "203.0.113.7",
            userAgent: "Firefox on Linux",
            current: true,
          },
          {
            id: "s-other",
            createdAt: new Date().toISOString(),
            lastSeenAt: new Date().toISOString(),
            ip: "198.51.100.4",
            userAgent: "curl/8",
            current: false,
          },
        ],
      }),
    );
    render(<SettingsPage />);

    expect(screen.getByRole("table")).toBeTruthy();
    expect(screen.getByText("Firefox on Linux")).toBeTruthy();
    expect(screen.getByText("curl/8")).toBeTruthy();
    // Exactly one "This session" badge; the other row is revocable.
    expect(screen.getAllByText("This session")).toHaveLength(1);
    expect(screen.getByRole("button", { name: "Revoke" })).toBeTruthy();
  });
});

describe("Settings change-password — client-side guards", () => {
  beforeEach(() => {
    mockUseSessions.mockReturnValue(query({ data: [] }));
  });

  it("blocks submit and shows a length error when the new password is too short", () => {
    const changePassword = mutation();
    mockUseChangePassword.mockReturnValue(changePassword);
    render(<SettingsPage />);

    fireEvent.change(screen.getByLabelText("Current password"), { target: { value: "old-password-1" } });
    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "short" } });
    fireEvent.change(screen.getByLabelText("Confirm new password"), { target: { value: "short" } });
    fireEvent.click(screen.getByRole("button", { name: "Change password" }));

    expect((changePassword as unknown as { mutate: ReturnType<typeof vi.fn> }).mutate).not.toHaveBeenCalled();
    // The submit error is distinct from the always-present "At least 12 characters" hint.
    expect(screen.getByText("New password must be at least 12 characters.")).toBeTruthy();
  });

  it("blocks submit when confirmation does not match", () => {
    const changePassword = mutation();
    mockUseChangePassword.mockReturnValue(changePassword);
    render(<SettingsPage />);

    fireEvent.change(screen.getByLabelText("Current password"), { target: { value: "old-password-1" } });
    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "a-brand-new-passphrase" } });
    fireEvent.change(screen.getByLabelText("Confirm new password"), { target: { value: "different-passphrase-x" } });
    fireEvent.click(screen.getByRole("button", { name: "Change password" }));

    expect((changePassword as unknown as { mutate: ReturnType<typeof vi.fn> }).mutate).not.toHaveBeenCalled();
    // Shown both as the live inline hint and the submit error — assert >= 1.
    expect(screen.getAllByText("New passwords do not match.").length).toBeGreaterThan(0);
  });

  it("submits the mutation when the form is valid", () => {
    const mutate = vi.fn();
    mockUseChangePassword.mockReturnValue(mutation({ mutate }));
    render(<SettingsPage />);

    fireEvent.change(screen.getByLabelText("Current password"), { target: { value: "old-password-1" } });
    fireEvent.change(screen.getByLabelText("New password"), { target: { value: "a-brand-new-passphrase" } });
    fireEvent.change(screen.getByLabelText("Confirm new password"), { target: { value: "a-brand-new-passphrase" } });
    fireEvent.click(screen.getByRole("button", { name: "Change password" }));

    expect(mutate).toHaveBeenCalledTimes(1);
    expect(mutate).toHaveBeenCalledWith(
      { currentPassword: "old-password-1", newPassword: "a-brand-new-passphrase" },
      expect.anything(),
    );
  });
});
