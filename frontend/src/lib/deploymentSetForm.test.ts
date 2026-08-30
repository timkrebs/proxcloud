// Deployment-set create-form tests — the rules mirror the backend's
// buildSetProvision + deploy.Validate, so every case here has a server-side twin.
import { describe, expect, it } from "vitest";

import type { CatalogRole, CatalogService } from "@/lib/api/generated/types";
import {
  agentBounds,
  allocateSetVmids,
  clampAgentCount,
  findRole,
  parseSshKeys,
  setTotals,
  toCreateSetRequest,
  validateSetForm,
  type SetFormState,
} from "@/lib/deploymentSetForm";

function role(name: string, over: Partial<CatalogRole> = {}): CatalogRole {
  return {
    name,
    count: 0,
    min: 0,
    max: 0,
    sizing: {
      default: { cores: 2, memoryMb: 2048, diskGb: 20 },
      min: { cores: 1, memoryMb: 512, diskGb: 10 },
    },
    ...over,
  };
}

function svc(over: Partial<CatalogService> = {}): CatalogService {
  return {
    id: "k3s",
    displayName: "K3s cluster",
    description: "",
    icon: "k8s",
    category: "Containers",
    kind: "set",
    guestType: "qemu",
    sizing: {
      default: { cores: 2, memoryMb: 2048, diskGb: 20 },
      min: { cores: 1, memoryMb: 512, diskGb: 10 },
    },
    roles: [
      role("server", {
        count: 1,
        min: 1,
        max: 1,
        sizing: {
          default: { cores: 2, memoryMb: 4096, diskGb: 20 },
          min: { cores: 1, memoryMb: 1024, diskGb: 10 },
        },
      }),
      role("agent", {
        count: 2,
        min: 1,
        max: 5,
        sizing: {
          default: { cores: 2, memoryMb: 2048, diskGb: 20 },
          min: { cores: 1, memoryMb: 512, diskGb: 10 },
        },
      }),
    ],
    credentials: [],
    ports: [6443],
    readiness: "",
    docs: "",
    testedOn: "",
    ...over,
  };
}

function validForm(over: Partial<SetFormState> = {}): SetFormState {
  return {
    serviceId: "k3s",
    name: "k3s-prod",
    projectId: "p1",
    projectName: "Prod",
    node: "pve01",
    storage: "local-lvm",
    bridge: "vmbr0",
    vlanTag: "",
    firewall: false,
    agentCount: 2,
    startVmid: "200",
    cidr: "192.168.1.50/24",
    gateway: "192.168.1.1",
    sshKeys: "ssh-ed25519 AAAAExampleKey user@host",
    tags: [],
    ...over,
  };
}

const BOUNDS = agentBounds(findRole(svc(), "agent"));

describe("agentBounds", () => {
  it("uses the role's min/max/default when set", () => {
    expect(agentBounds(role("agent", { count: 2, min: 1, max: 5 }))).toEqual({
      min: 1,
      max: 5,
      default: 2,
    });
  });
  it("falls back a zero min/max to the default count (mirrors the backend)", () => {
    expect(agentBounds(role("agent", { count: 3, min: 0, max: 0 }))).toEqual({
      min: 3,
      max: 3,
      default: 3,
    });
  });
  it("treats a missing role as a single default worker", () => {
    expect(agentBounds(undefined)).toEqual({ min: 1, max: 1, default: 1 });
  });
});

describe("clampAgentCount", () => {
  it("defaults a zero/negative request to the role default", () => {
    expect(clampAgentCount(0, BOUNDS)).toBe(2);
    expect(clampAgentCount(-3, BOUNDS)).toBe(2);
  });
  it("clamps below min up to min", () => {
    expect(clampAgentCount(1, { min: 2, max: 5, default: 3 })).toBe(2);
  });
  it("clamps above max down to max", () => {
    expect(clampAgentCount(99, BOUNDS)).toBe(5);
  });
  it("passes an in-range value through (floored)", () => {
    expect(clampAgentCount(3, BOUNDS)).toBe(3);
    expect(clampAgentCount(3.9, BOUNDS)).toBe(3);
  });
});

describe("allocateSetVmids", () => {
  it("returns agentCount + 1 distinct ids; agents length === count", () => {
    const { serverVmid, agentVmids } = allocateSetVmids(200, 3);
    expect(serverVmid).toBe(200);
    expect(agentVmids).toEqual([201, 202, 203]);
    expect(agentVmids).toHaveLength(3);
    expect(new Set([serverVmid, ...agentVmids]).size).toBe(4);
  });

  it("skips VMIDs already in use", () => {
    const { serverVmid, agentVmids } = allocateSetVmids(200, 2, new Set([201, 202]));
    expect(serverVmid).toBe(200);
    expect(agentVmids).toEqual([203, 204]);
  });

  it("handles a zero worker count (server only)", () => {
    const { serverVmid, agentVmids } = allocateSetVmids(500, 0);
    expect(serverVmid).toBe(500);
    expect(agentVmids).toEqual([]);
  });
});

describe("setTotals", () => {
  it("sums the server plus N agents' default sizing", () => {
    // server: 2c/4096/20 ; agent: 2c/2048/20 ; 2 agents
    expect(setTotals(svc(), 2)).toEqual({ vcpu: 6, ramMb: 8192, diskGb: 60, count: 3 });
  });
});

describe("parseSshKeys", () => {
  it("splits, trims, and drops blank lines", () => {
    expect(parseSshKeys("  a \n\n b \n")).toEqual(["a", "b"]);
    expect(parseSshKeys("   ")).toEqual([]);
  });
});

describe("validateSetForm", () => {
  it("accepts a complete, valid form", () => {
    expect(validateSetForm(validForm(), BOUNDS)).toEqual([]);
  });

  const fieldCases: [string, Partial<SetFormState>, string][] = [
    ["missing name", { name: "" }, "name"],
    ["bad name chars", { name: "K3s Prod" }, "name"],
    ["name too long for suffix", { name: "a".repeat(38) }, "name"],
    ["missing project", { projectId: "" }, "projectId"],
    ["missing node", { node: "" }, "node"],
    ["missing storage", { storage: "" }, "storage"],
    ["missing bridge", { bridge: "" }, "bridge"],
    ["bad vlan", { vlanTag: "9999" }, "vlanTag"],
    ["bad cidr", { cidr: "192.168.1.50" }, "cidr"],
    ["missing gateway", { gateway: "" }, "gateway"],
    ["bad gateway", { gateway: "not-an-ip" }, "gateway"],
    ["no ssh key", { sshKeys: "   \n  " }, "sshKeys"],
    ["bad start vmid", { startVmid: "42" }, "startVmid"],
    ["non-integer start vmid", { startVmid: "abc" }, "startVmid"],
    ["bad tag", { tags: ["Bad Tag"] }, "tags"],
  ];
  it.each(fieldCases)("flags %s on field %s", (_label, over, field) => {
    const errs = validateSetForm(validForm(over), BOUNDS);
    expect(errs.some((e) => e.field === field)).toBe(true);
  });

  it("flags an agentCount outside the role bounds", () => {
    const errs = validateSetForm(validForm({ agentCount: 9 }), BOUNDS);
    expect(errs.some((e) => e.field === "agentCount")).toBe(true);
  });

  it("flags a start VMID that collides with an in-use id", () => {
    const errs = validateSetForm(validForm({ startVmid: "200" }), BOUNDS, new Set([200]));
    expect(errs.some((e) => e.field === "startVmid")).toBe(true);
  });
});

describe("toCreateSetRequest", () => {
  it("shapes a valid request: clamped count, agentVmids length === count, static serverIp", () => {
    const req = toCreateSetRequest(validForm({ agentCount: 2, startVmid: "300" }), BOUNDS);
    expect(req.serviceId).toBe("k3s");
    expect(req.projectId).toBe("p1");
    expect(req.name).toBe("k3s-prod");
    expect(req.node).toBe("pve01");
    expect(req.storage).toBe("local-lvm");
    expect(req.bridge).toBe("vmbr0");
    expect(req.agentCount).toBe(2);
    expect(req.serverVmid).toBe(300);
    expect(req.agentVmids).toEqual([301, 302]);
    expect(req.agentVmids).toHaveLength(req.agentCount!);
    expect(req.serverIp).toEqual({
      mode: "static",
      cidr: "192.168.1.50/24",
      gateway: "192.168.1.1",
    });
    expect(req.sshKeys).toEqual(["ssh-ed25519 AAAAExampleKey user@host"]);
  });

  it("clamps an over-max worker count and keeps agentVmids in lockstep", () => {
    const req = toCreateSetRequest(validForm({ agentCount: 99, startVmid: "400" }), BOUNDS);
    expect(req.agentCount).toBe(5); // max
    expect(req.agentVmids).toHaveLength(5);
    expect(req.serverVmid).toBe(400);
    expect(req.agentVmids).toEqual([401, 402, 403, 404, 405]);
  });

  it("omits vlanTag when blank and includes it when set", () => {
    expect(toCreateSetRequest(validForm(), BOUNDS).vlanTag).toBeUndefined();
    expect(toCreateSetRequest(validForm({ vlanTag: "42" }), BOUNDS).vlanTag).toBe(42);
  });

  it("routes the VMID block around taken ids", () => {
    const req = toCreateSetRequest(
      validForm({ agentCount: 2, startVmid: "500" }),
      BOUNDS,
      new Set([501]),
    );
    expect(req.serverVmid).toBe(500);
    expect(req.agentVmids).toEqual([502, 503]);
  });
});
