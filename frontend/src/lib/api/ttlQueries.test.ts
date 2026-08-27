// TTL query mapping — a guest-TTL GET that 404s is "no TTL" (null, a real empty
// state), not an error; any other failure propagates. apiFetch is mocked so no
// network is touched. Mirrors the schedule 404→null contract.
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("@/lib/stores/toastStore", () => ({ pushToast: vi.fn() }));
vi.mock("@/lib/api/client", () => {
  class ApiError extends Error {
    code: string;
    status: number;
    constructor(status: number, body: { code: string; message: string }) {
      super(body.message);
      this.code = body.code;
      this.status = status;
    }
    get detail() {
      return this.message;
    }
  }
  return { apiFetch: vi.fn(), ApiError };
});

import { ApiError, apiFetch } from "@/lib/api/client";
import { getTtlOrNull } from "@/lib/api/ttlQueries";
import type { Ttl } from "@/lib/api/generated/types";

const mockApiFetch = vi.mocked(apiFetch);

const sampleTtl: Ttl = {
  id: "ttl1",
  tenantId: "t1",
  projectId: "p1",
  vmid: 101,
  expiresAt: "2026-09-01T00:00:00Z",
  action: "stop",
  warned24h: false,
  warned1h: false,
  originalDurationSeconds: 86400,
  createdAt: "2026-08-01T00:00:00Z",
  updatedAt: "2026-08-01T00:00:00Z",
};

afterEach(() => vi.clearAllMocks());

describe("getTtlOrNull", () => {
  it("returns the TTL when the GET succeeds", async () => {
    mockApiFetch.mockResolvedValueOnce(sampleTtl);
    await expect(getTtlOrNull("/api/tenants/t1/guests/pve/qemu/101/ttl")).resolves.toEqual(
      sampleTtl,
    );
  });

  it("maps a 404 to null (no TTL — a real empty state)", async () => {
    mockApiFetch.mockRejectedValueOnce(new ApiError(404, { code: "not_found", message: "no ttl" }));
    await expect(getTtlOrNull("/api/tenants/t1/guests/pve/qemu/101/ttl")).resolves.toBeNull();
  });

  it("propagates a non-404 API error", async () => {
    mockApiFetch.mockRejectedValueOnce(new ApiError(500, { code: "internal", message: "boom" }));
    await expect(getTtlOrNull("/api/tenants/t1/guests/pve/qemu/101/ttl")).rejects.toBeInstanceOf(
      ApiError,
    );
  });

  it("propagates a non-API error (e.g. network)", async () => {
    mockApiFetch.mockRejectedValueOnce(new Error("network down"));
    await expect(getTtlOrNull("/api/tenants/t1/guests/pve/qemu/101/ttl")).rejects.toThrow(
      "network down",
    );
  });
});
