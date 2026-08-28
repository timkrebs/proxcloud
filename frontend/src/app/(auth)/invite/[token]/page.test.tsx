// Invite-accept screen — the public InvitationDetails fetch drives a three-way
// branch (new account / one-click attach / sign-in-to-accept) plus the
// invalid(404) state, and accept failures map to the documented codes. apiFetch,
// the router, and params are mocked so each branch renders deterministically.
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { InvitationDetails } from "@/lib/api/generated/types";

const push = vi.fn();
vi.mock("next/navigation", () => ({
  useRouter: () => ({ push }),
  useParams: () => ({ token: "tok-123" }),
}));
vi.mock("next/link", () => ({
  default: ({
    children,
    href,
    className,
  }: {
    children: React.ReactNode;
    href: unknown;
    className?: string;
  }) => (
    <a href={typeof href === "string" ? href : "#"} className={className}>
      {children}
    </a>
  ),
}));
vi.mock("@/lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api/client")>();
  return { ...actual, apiFetch: vi.fn() };
});

import InviteAcceptPage from "./page";
import { ApiError, apiFetch } from "@/lib/api/client";

const mockFetch = vi.mocked(apiFetch);

const DETAILS: InvitationDetails = {
  email: "invitee@example.com",
  tenantName: "Acme",
  scopeType: "tenant",
  scopeLabel: "Acme (all projects)",
  role: "contributor",
  expiresAt: "2026-09-01T00:00:00Z",
  requiresAccount: false,
  signedInMatches: false,
};

afterEach(() => cleanup());
beforeEach(() => vi.clearAllMocks());

/** First call is the GET details; resolve it with the given shape. */
function resolveDetails(details: InvitationDetails) {
  mockFetch.mockResolvedValueOnce(details);
}

describe("Invite accept — load states", () => {
  it("renders the invalid card on a 404 details fetch", async () => {
    mockFetch.mockRejectedValueOnce(new ApiError(404, { code: "not_found", message: "no" }));
    render(<InviteAcceptPage />);
    expect(await screen.findByText("Invitation unavailable")).toBeTruthy();
  });

  it("renders a retryable error card on a non-404 failure", async () => {
    mockFetch.mockRejectedValueOnce(new ApiError(500, { code: "internal", message: "boom" }));
    render(<InviteAcceptPage />);
    expect(await screen.findByRole("button", { name: "Retry" })).toBeTruthy();
  });
});

describe("Invite accept — branching", () => {
  it("new-account branch collects name + password and accepts", async () => {
    resolveDetails({ ...DETAILS, requiresAccount: true });
    mockFetch.mockResolvedValueOnce(undefined); // accept → 204
    render(<InviteAcceptPage />);

    await screen.findByText("Create your account");
    fireEvent.change(screen.getByLabelText("Full name"), { target: { value: "New Person" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "a-strong-password" } });
    fireEvent.change(screen.getByLabelText("Confirm password"), {
      target: { value: "a-strong-password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create account & accept" }));

    await waitFor(() => expect(push).toHaveBeenCalledWith("/dashboard"));
    expect(mockFetch.mock.calls[1][0]).toBe("/api/auth/invitations/tok-123/accept");
  });

  it("new-account branch blocks a too-short password before any request", async () => {
    resolveDetails({ ...DETAILS, requiresAccount: true });
    render(<InviteAcceptPage />);

    await screen.findByText("Create your account");
    fireEvent.change(screen.getByLabelText("Full name"), { target: { value: "New Person" } });
    fireEvent.change(screen.getByLabelText("Password"), { target: { value: "short" } });
    fireEvent.change(screen.getByLabelText("Confirm password"), { target: { value: "short" } });
    fireEvent.click(screen.getByRole("button", { name: "Create account & accept" }));

    expect(screen.getByText("Password must be at least 12 characters.")).toBeTruthy();
    // Only the GET happened — no accept POST.
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });

  it("attach branch shows a one-click Accept and maps account_exists", async () => {
    resolveDetails({ ...DETAILS, requiresAccount: false, signedInMatches: true });
    mockFetch.mockRejectedValueOnce(
      new ApiError(409, { code: "account_exists", message: "exists" }),
    );
    render(<InviteAcceptPage />);

    const accept = await screen.findByRole("button", { name: "Accept invitation" });
    fireEvent.click(accept);

    expect(await screen.findByText(/An account already exists/)).toBeTruthy();
    expect(push).not.toHaveBeenCalled();
  });

  it("existing-account-not-signed-in branch links to sign-in with a returnTo", async () => {
    resolveDetails({ ...DETAILS, requiresAccount: false, signedInMatches: false });
    render(<InviteAcceptPage />);

    const link = await screen.findByRole("link", { name: "Sign in to accept" });
    expect(link.getAttribute("href")).toBe(
      `/signin?returnTo=${encodeURIComponent("/invite/tok-123")}`,
    );
  });
});
