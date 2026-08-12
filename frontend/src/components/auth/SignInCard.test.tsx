// SignInCard — the Phase-5 second factor. Login now returns 200
// LoginResponse{totpRequired}. When false, sign-in completes as before; when
// true, the card advances to a TOTP step that posts to /api/auth/login/totp and
// branches on the two 401 codes (bad code vs. expired challenge). apiFetch and
// the router are mocked so every branch is exercised without a network.
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const push = vi.fn();
vi.mock("next/navigation", () => ({ useRouter: () => ({ push }) }));

// Mock apiFetch but keep the real ApiError so error branches classify correctly.
vi.mock("@/lib/api/client", async (importOriginal) => {
  const actual = await importOriginal<typeof import("@/lib/api/client")>();
  return { ...actual, apiFetch: vi.fn() };
});

import { SignInCard, resolveReturnTo } from "@/components/auth/SignInCard";
import { ApiError, apiFetch } from "@/lib/api/client";

const mockFetch = vi.mocked(apiFetch);

afterEach(() => cleanup());
beforeEach(() => {
  vi.clearAllMocks();
});

/** Drive the card from the email step to a submitted password. */
function signInWithPassword() {
  fireEvent.change(screen.getByLabelText("Email"), { target: { value: "user@example.com" } });
  fireEvent.click(screen.getByRole("button", { name: "Next" }));
  fireEvent.change(screen.getByLabelText("Password"), { target: { value: "a-strong-password" } });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
}

describe("resolveReturnTo", () => {
  it("accepts a same-origin relative path", () => {
    expect(resolveReturnTo("/invite/abc")).toBe("/invite/abc");
  });
  it("rejects protocol-relative and absolute URLs", () => {
    expect(resolveReturnTo("//evil.example")).toBe("/dashboard");
    expect(resolveReturnTo("https://evil.example")).toBe("/dashboard");
  });
  it("defaults to the dashboard when absent", () => {
    expect(resolveReturnTo(null)).toBe("/dashboard");
  });
});

describe("SignInCard — login without TOTP", () => {
  it("goes straight to the dashboard when totpRequired is false", async () => {
    mockFetch.mockResolvedValueOnce({ totpRequired: false });
    render(<SignInCard />);
    signInWithPassword();

    await waitFor(() => expect(push).toHaveBeenCalledWith("/dashboard"));
    expect(mockFetch).toHaveBeenCalledTimes(1);
    expect(mockFetch.mock.calls[0][0]).toBe("/api/auth/login");
  });

  it("shows a generic message on a 401 and does not advance", async () => {
    mockFetch.mockRejectedValueOnce(new ApiError(401, { code: "unauthenticated", message: "no" }));
    render(<SignInCard />);
    signInWithPassword();

    expect(await screen.findByText("Incorrect email or password.")).toBeTruthy();
    expect(push).not.toHaveBeenCalled();
  });
});

describe("SignInCard — TOTP step", () => {
  it("advances to the TOTP step when totpRequired is true (no redirect yet)", async () => {
    mockFetch.mockResolvedValueOnce({ totpRequired: true });
    render(<SignInCard />);
    signInWithPassword();

    expect(await screen.findByText("Two-step verification")).toBeTruthy();
    expect(push).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Verify" })).toBeTruthy();
  });

  it("completes sign-in when the code verifies", async () => {
    mockFetch.mockResolvedValueOnce({ totpRequired: true }); // /login
    mockFetch.mockResolvedValueOnce(undefined); // /login/totp → 204
    render(<SignInCard />);
    signInWithPassword();

    await screen.findByText("Two-step verification");
    fireEvent.change(screen.getByLabelText("Verification code"), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Verify" }));

    await waitFor(() => expect(push).toHaveBeenCalledWith("/dashboard"));
    expect(mockFetch.mock.calls[1][0]).toBe("/api/auth/login/totp");
    expect(mockFetch.mock.calls[1][1]).toMatchObject({
      body: JSON.stringify({ code: "123456" }),
    });
  });

  it("keeps the user on the TOTP step with a message on a bad code", async () => {
    mockFetch.mockResolvedValueOnce({ totpRequired: true });
    mockFetch.mockRejectedValueOnce(new ApiError(401, { code: "unauthenticated", message: "no" }));
    render(<SignInCard />);
    signInWithPassword();

    await screen.findByText("Two-step verification");
    fireEvent.change(screen.getByLabelText("Verification code"), { target: { value: "000000" } });
    fireEvent.click(screen.getByRole("button", { name: "Verify" }));

    expect(await screen.findByText("That code is not valid. Try again.")).toBeTruthy();
    expect(push).not.toHaveBeenCalled();
    // Still on the TOTP step.
    expect(screen.getByRole("button", { name: "Verify" })).toBeTruthy();
  });

  it("restarts from the email step when the challenge has expired", async () => {
    mockFetch.mockResolvedValueOnce({ totpRequired: true });
    mockFetch.mockRejectedValueOnce(
      new ApiError(401, { code: "totp_challenge_expired", message: "expired" }),
    );
    render(<SignInCard />);
    signInWithPassword();

    await screen.findByText("Two-step verification");
    fireEvent.change(screen.getByLabelText("Verification code"), { target: { value: "123456" } });
    fireEvent.click(screen.getByRole("button", { name: "Verify" }));

    // Back to the email step, with a visible notice (no toast host in (auth)).
    expect(await screen.findByText("Your sign-in timed out. Enter your password again.")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Next" })).toBeTruthy();
    expect(push).not.toHaveBeenCalled();
  });

  it("relaxes the input when switching to a recovery code", async () => {
    mockFetch.mockResolvedValueOnce({ totpRequired: true });
    render(<SignInCard />);
    signInWithPassword();

    await screen.findByText("Two-step verification");
    // TOTP mode strips non-digits.
    const totpInput = screen.getByLabelText("Verification code") as HTMLInputElement;
    fireEvent.change(totpInput, { target: { value: "12ab34" } });
    expect(totpInput.value).toBe("1234");

    fireEvent.click(screen.getByRole("button", { name: "Use a recovery code instead" }));
    const recoveryInput = screen.getByLabelText("Recovery code") as HTMLInputElement;
    fireEvent.change(recoveryInput, { target: { value: "ABCDE-12345" } });
    expect(recoveryInput.value).toBe("ABCDE-12345");
  });
});
