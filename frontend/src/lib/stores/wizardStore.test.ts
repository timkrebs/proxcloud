// Wizard validation + wire-request tests — the rules mirror the backend's
// deploy.Validate, so every case here has a server-side twin.
import { beforeEach, describe, expect, it } from "vitest";

import type { ProjectQuotaResponse, QuotaLimits, QuotaUsage } from "@/lib/api/generated/types";
import {
  effectiveRemaining,
  toCreateRequest,
  useWizardStore,
  validateWizard,
  type QuotaRemaining,
  type WizardState,
} from "@/lib/stores/wizardStore";

function validLxc(): WizardState {
  const s = useWizardStore.getState();
  s.init("lxc");
  s.set({
    name: "cache-01",
    node: "pve01",
    vmid: "200",
    projectId: "p-web",
    projectName: "Web",
    vztmplVolId: "local:vztmpl/debian-12.tar.zst",
    storage: "local-lvm",
    bridge: "vmbr0",
  });
  return useWizardStore.getState();
}

function validVm(): WizardState {
  const s = useWizardStore.getState();
  s.init("qemu");
  s.set({
    name: "web-02",
    node: "pve01",
    vmid: "106",
    projectId: "p-web",
    projectName: "Web",
    isoVolId: "local:iso/debian-12.iso",
    storage: "local-lvm",
    bridge: "vmbr0",
  });
  return useWizardStore.getState();
}

beforeEach(() => {
  useWizardStore.getState().init("qemu");
});

describe("validateWizard", () => {
  it("passes a complete LXC config", () => {
    expect(validateWizard(validLxc())).toEqual([]);
  });

  it("passes a complete VM config", () => {
    expect(validateWizard(validVm())).toEqual([]);
  });

  const nameCases: [string, boolean][] = [
    ["web-02", true],
    ["a", true],
    ["Web-02", false], // uppercase
    ["2web", false], // starts with digit
    ["web_02", false], // underscore
    ["", false],
    ["a".repeat(41), false], // too long
  ];
  it.each(nameCases)("name %j valid=%s", (name, ok) => {
    const s = validVm();
    useWizardStore.getState().set({ name });
    const errs = validateWizard(useWizardStore.getState());
    expect(errs.some((e) => e.field === "name")).toBe(!ok);
    void s;
  });

  const vmidCases: [string, boolean][] = [
    ["100", true],
    ["999999999", true],
    ["99", false],
    ["1000000000", false],
    ["abc", false],
    ["", false],
    ["106.5", false],
  ];
  it.each(vmidCases)("vmid %j valid=%s", (vmid, ok) => {
    validVm();
    useWizardStore.getState().set({ vmid });
    const errs = validateWizard(useWizardStore.getState());
    expect(errs.some((e) => e.field === "vmid")).toBe(!ok);
  });

  it("rejects an already-used VMID", () => {
    validVm();
    const errs = validateWizard(useWizardStore.getState(), [106, 200]);
    expect(errs.some((e) => e.field === "vmid" && e.message.includes("already in use"))).toBe(true);
  });

  it("requires a project (Basics, tab 0)", () => {
    validVm();
    useWizardStore.getState().set({ projectId: "" });
    const errs = validateWizard(useWizardStore.getState());
    expect(errs.some((e) => e.field === "projectId" && e.tab === 0)).toBe(true);
    // Choosing a project clears the error.
    useWizardStore.getState().set({ projectId: "p-web" });
    expect(validateWizard(useWizardStore.getState()).some((e) => e.field === "projectId")).toBe(false);
  });

  it("requires a template for LXC", () => {
    validLxc();
    useWizardStore.getState().set({ vztmplVolId: "" });
    expect(validateWizard(useWizardStore.getState()).some((e) => e.field === "vztmplVolId")).toBe(true);
  });

  it("requires ISO xor clone source for VM", () => {
    validVm();
    useWizardStore.getState().set({ isoVolId: "" });
    expect(validateWizard(useWizardStore.getState()).some((e) => e.field === "isoVolId")).toBe(true);

    useWizardStore.getState().set({ sourceMode: "clone", cloneVmid: null });
    expect(validateWizard(useWizardStore.getState()).some((e) => e.field === "cloneVmid")).toBe(true);

    useWizardStore.getState().set({ cloneVmid: 9000 });
    expect(validateWizard(useWizardStore.getState()).some((e) => e.tab === 1)).toBe(false);
  });

  const boundCases: [Partial<WizardState>, string][] = [
    [{ cores: "0" }, "cores"],
    [{ cores: "129" }, "cores"],
    [{ cores: "abc" }, "cores"],
    [{ memoryMb: "64" }, "memoryMb"],
    [{ diskGb: "0" }, "diskGb"],
    [{ storage: "" }, "storage"],
    [{ bridge: "" }, "bridge"],
    [{ vlanTag: "5000" }, "vlanTag"],
    [{ vlanTag: "0" }, "vlanTag"],
  ];
  it.each(boundCases)("bounds %j flags %s", (patch, field) => {
    validVm();
    useWizardStore.getState().set(patch);
    expect(validateWizard(useWizardStore.getState()).some((e) => e.field === field)).toBe(true);
  });

  it("validates static IP config", () => {
    validVm();
    useWizardStore.getState().set({ ipMode: "static", cidr: "not-a-cidr" });
    expect(validateWizard(useWizardStore.getState()).some((e) => e.field === "cidr")).toBe(true);

    useWizardStore.getState().set({ cidr: "192.168.1.50/24", gateway: "not-an-ip" });
    expect(validateWizard(useWizardStore.getState()).some((e) => e.field === "gateway")).toBe(true);

    useWizardStore.getState().set({ gateway: "192.168.1.1" });
    expect(validateWizard(useWizardStore.getState())).toEqual([]);
  });

  it("validates tag charset", () => {
    validVm();
    useWizardStore.getState().set({ tags: ["ok-tag", "Bad Tag"] });
    expect(validateWizard(useWizardStore.getState()).some((e) => e.field === "tags")).toBe(true);
  });

  it("skips disk/storage/bridge checks for clones", () => {
    validVm();
    useWizardStore.getState().set({ sourceMode: "clone", cloneVmid: 9000, storage: "", bridge: "", diskGb: "" });
    expect(validateWizard(useWizardStore.getState())).toEqual([]);
  });
});

describe("validateWizard — quota (over-quota client feedback)", () => {
  const unlimited: QuotaRemaining = { vcpu: null, ramMb: null, diskGb: null, count: null };

  it("passes when no remaining is supplied (quota check is opt-in)", () => {
    validVm();
    expect(validateWizard(useWizardStore.getState())).toEqual([]);
  });

  it("passes when remaining dimensions are unlimited (null)", () => {
    validVm();
    useWizardStore.getState().set({ cores: "64", memoryMb: "999999", diskGb: "9999" });
    const quotaErrs = validateWizard(useWizardStore.getState(), [], unlimited).filter((e) => e.tab === 2 && e.field !== "cores");
    // cores 64 is within the 1–128 base bound, so only quota (none here) matters.
    expect(quotaErrs).toEqual([]);
  });

  it("flags cores over the remaining vCPU", () => {
    validVm();
    useWizardStore.getState().set({ cores: "8" });
    const errs = validateWizard(useWizardStore.getState(), [], { ...unlimited, vcpu: 4 });
    expect(errs.some((e) => e.field === "cores" && e.tab === 2 && e.message.includes("4 vCPU remaining"))).toBe(true);
  });

  it("allows cores exactly at the remaining vCPU", () => {
    validVm();
    useWizardStore.getState().set({ cores: "4" });
    const errs = validateWizard(useWizardStore.getState(), [], { ...unlimited, vcpu: 4 });
    expect(errs.some((e) => e.field === "cores")).toBe(false);
  });

  it("flags memory over the remaining RAM (MiB)", () => {
    validVm();
    useWizardStore.getState().set({ memoryMb: "4096" });
    const errs = validateWizard(useWizardStore.getState(), [], { ...unlimited, ramMb: 2048 });
    expect(errs.some((e) => e.field === "memoryMb" && e.tab === 2)).toBe(true);
  });

  it("flags disk over the remaining disk for a non-clone", () => {
    validVm();
    useWizardStore.getState().set({ diskGb: "100" });
    const errs = validateWizard(useWizardStore.getState(), [], { ...unlimited, diskGb: 50 });
    expect(errs.some((e) => e.field === "diskGb" && e.tab === 2)).toBe(true);
  });

  it("skips the disk quota check for clones (disk is template-derived)", () => {
    validVm();
    useWizardStore.getState().set({ sourceMode: "clone", cloneVmid: 9000, diskGb: "100", storage: "", bridge: "" });
    const errs = validateWizard(useWizardStore.getState(), [], { ...unlimited, diskGb: 1 });
    expect(errs.some((e) => e.field === "diskGb")).toBe(false);
  });

  it("skips vCPU and RAM quota checks for clones (delta is template-derived)", () => {
    validVm();
    useWizardStore.getState().set({ sourceMode: "clone", cloneVmid: 9000, cores: "8", memoryMb: "8192", storage: "", bridge: "" });
    const errs = validateWizard(useWizardStore.getState(), [], { ...unlimited, vcpu: 1, ramMb: 1 });
    expect(errs.some((e) => e.field === "cores")).toBe(false);
    expect(errs.some((e) => e.field === "memoryMb")).toBe(false);
  });

  it("still flags an exhausted guest-count quota for clones", () => {
    validVm();
    useWizardStore.getState().set({ sourceMode: "clone", cloneVmid: 9000, storage: "", bridge: "" });
    const errs = validateWizard(useWizardStore.getState(), [], { ...unlimited, count: 0 });
    expect(errs.some((e) => e.field === "quota" && e.tab === 2)).toBe(true);
  });

  it("flags an exhausted guest-count quota", () => {
    validVm();
    const errs = validateWizard(useWizardStore.getState(), [], { ...unlimited, count: 0 });
    expect(errs.some((e) => e.field === "quota" && e.tab === 2)).toBe(true);
  });

  it("passes a config that fits inside every remaining dimension", () => {
    validVm();
    useWizardStore.getState().set({ cores: "2", memoryMb: "2048", diskGb: "32" });
    const errs = validateWizard(useWizardStore.getState(), [], {
      vcpu: 4,
      ramMb: 4096,
      diskGb: 64,
      count: 3,
    });
    expect(errs).toEqual([]);
  });
});

describe("effectiveRemaining", () => {
  const usage: QuotaUsage = { vcpu: 0, ramMb: 0, diskGb: 0, count: 0 };
  const mk = (limits: QuotaLimits, remaining: QuotaUsage): ProjectQuotaResponse["project"] => ({
    scopeType: "project",
    scopeId: "s",
    limits,
    usage,
    remaining,
  });

  it("takes the tighter of project vs tenant per dimension", () => {
    const resp: ProjectQuotaResponse = {
      project: mk({ maxVcpu: 10 }, { ...usage, vcpu: 6 }),
      tenant: mk({ maxVcpu: 8 }, { ...usage, vcpu: 3 }),
    };
    expect(effectiveRemaining(resp).vcpu).toBe(3);
  });

  it("uses the only scope that caps a dimension", () => {
    const resp: ProjectQuotaResponse = {
      project: mk({}, { ...usage, vcpu: 999 }),
      tenant: mk({ maxVcpu: 8 }, { ...usage, vcpu: 5 }),
    };
    expect(effectiveRemaining(resp).vcpu).toBe(5);
  });

  it("is unlimited (null) where neither scope caps the dimension", () => {
    const resp: ProjectQuotaResponse = {
      project: mk({}, usage),
      tenant: mk({}, usage),
    };
    const r = effectiveRemaining(resp);
    expect(r).toEqual({ vcpu: null, ramMb: null, diskGb: null, count: null });
  });
});

describe("tab gating", () => {
  it("locks tabs beyond maxTab and unlocks via next()", () => {
    const s = useWizardStore.getState();
    s.init("qemu");
    expect(useWizardStore.getState().maxTab).toBe(0);

    useWizardStore.getState().goTab(3);
    expect(useWizardStore.getState().tab).toBe(0); // locked — no jump

    useWizardStore.getState().next();
    useWizardStore.getState().next();
    expect(useWizardStore.getState().tab).toBe(2);
    expect(useWizardStore.getState().maxTab).toBe(2);

    useWizardStore.getState().goTab(1); // revisiting unlocked tab works
    expect(useWizardStore.getState().tab).toBe(1);
  });
});

describe("toCreateRequest", () => {
  it("builds the LXC wire request with projectId and no pool", () => {
    validLxc();
    useWizardStore.getState().set({
      vlanTag: "20",
      firewall: true,
      ipMode: "static",
      cidr: "192.168.1.50/24",
      gateway: "192.168.1.1",
      sshKeys: "ssh-ed25519 AAA key\n\n",
      tags: ["env-prod"],
    });
    const req = toCreateRequest(useWizardStore.getState());
    expect(req).toMatchObject({
      type: "lxc",
      name: "cache-01",
      vmid: 200,
      projectId: "p-web",
      source: { mode: "vztmpl", vztmplVolId: "local:vztmpl/debian-12.tar.zst" },
      cores: 2,
      memoryMb: 2048,
      diskGb: 8,
      storage: "local-lvm",
      bridge: "vmbr0",
      vlanTag: 20,
      firewall: true,
      ipConfig: { mode: "static", cidr: "192.168.1.50/24", gateway: "192.168.1.1" },
      cloudInit: { sshKeys: ["ssh-ed25519 AAA key"] },
      tags: ["env-prod"],
      startAfterCreate: true,
    });
    // The deprecated client-supplied pool is gone — the backend derives it.
    expect("pool" in req).toBe(false);
  });

  it("linked clones omit storage; full clones keep it", () => {
    validVm();
    useWizardStore.getState().set({ sourceMode: "clone", cloneVmid: 9000, cloneMode: "linked" });
    expect(toCreateRequest(useWizardStore.getState()).storage).toBe("");

    useWizardStore.getState().set({ cloneMode: "full" });
    expect(toCreateRequest(useWizardStore.getState()).storage).toBe("local-lvm");
  });

  it("omits cloudInit when nothing is configured", () => {
    validVm();
    const req = toCreateRequest(useWizardStore.getState());
    expect(req.cloudInit).toBeUndefined();
  });
});
