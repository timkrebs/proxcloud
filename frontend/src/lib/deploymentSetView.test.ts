// Deployment-set view-helper tests — the honest display fallbacks when the
// durable GET/list carry no set/member name.
import { describe, expect, it } from "vitest";

import type { DeploymentSet, DeploymentSetMember } from "@/lib/api/generated/types";
import {
  hasLiveMembers,
  isSetTransitional,
  memberLabel,
  orderedMembers,
  serverMember,
  setBaseName,
} from "@/lib/deploymentSetView";

function member(over: Partial<DeploymentSetMember> = {}): DeploymentSetMember {
  return { role: "agent", vmid: 201, node: "pve01", guestType: "qemu", status: "active", ...over };
}

function set(over: Partial<DeploymentSet> = {}): DeploymentSet {
  return {
    id: "abcdef01-2345-6789-abcd-ef0123456789",
    serviceId: "k3s",
    projectId: "p1",
    status: "ready",
    createdAt: "2026-08-29T10:00:00Z",
    members: [],
    ...over,
  };
}

describe("setBaseName", () => {
  it("derives the base name from the server member (suffix stripped)", () => {
    const s = set({ members: [member({ role: "server", name: "k3s-prod-server", vmid: 200 })] });
    expect(setBaseName(s)).toBe("k3s-prod");
  });
  it("falls back to an agent's base name", () => {
    const s = set({ members: [member({ role: "agent", name: "k3s-prod-agent-2", vmid: 202 })] });
    expect(setBaseName(s)).toBe("k3s-prod");
  });
  it("falls back to a short id token when no member is named (durable GET)", () => {
    const s = set({ members: [member({ name: undefined })] });
    expect(setBaseName(s)).toBe("set-abcdef01");
  });
});

describe("memberLabel", () => {
  it("uses the member name when present", () => {
    expect(memberLabel(member({ name: "k3s-prod-agent-1" }))).toBe("k3s-prod-agent-1");
  });
  it("falls back to role + vmid", () => {
    expect(memberLabel(member({ name: undefined, role: "agent", vmid: 205 }))).toBe(
      "agent · vmid 205",
    );
  });
});

describe("orderedMembers", () => {
  it("sorts the server before its agents, then by VMID", () => {
    const s = set({
      members: [
        member({ role: "agent", vmid: 203 }),
        member({ role: "server", vmid: 200 }),
        member({ role: "agent", vmid: 201 }),
      ],
    });
    expect(orderedMembers(s).map((m) => m.vmid)).toEqual([200, 201, 203]);
  });
});

describe("hasLiveMembers", () => {
  it("is true when any member is not tombstoned", () => {
    expect(hasLiveMembers(set({ members: [member({ status: "active" })] }))).toBe(true);
  });
  it("is false when every member is tombstoned", () => {
    expect(
      hasLiveMembers(
        set({ members: [member({ status: "tombstoned" }), member({ status: "tombstoned" })] }),
      ),
    ).toBe(false);
  });
});

describe("isSetTransitional", () => {
  it("is true only for provisioning/deleting", () => {
    expect(isSetTransitional("provisioning")).toBe(true);
    expect(isSetTransitional("deleting")).toBe(true);
    expect(isSetTransitional("ready")).toBe(false);
    expect(isSetTransitional("failed")).toBe(false);
  });
});

describe("serverMember", () => {
  it("returns the control-plane member", () => {
    const s = set({
      members: [member({ role: "agent", vmid: 201 }), member({ role: "server", vmid: 200 })],
    });
    expect(serverMember(s)?.vmid).toBe(200);
  });
});
