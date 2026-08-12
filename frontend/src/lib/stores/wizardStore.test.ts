// Wizard validation + wire-request tests — the rules mirror the backend's
// deploy.Validate, so every case here has a server-side twin.
import { beforeEach, describe, expect, it } from "vitest";

import {
  toCreateRequest,
  useWizardStore,
  validateWizard,
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
