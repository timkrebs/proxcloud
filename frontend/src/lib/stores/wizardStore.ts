"use client";
// Create-wizard state — zustand store shared by all seven tabs, the sticky
// summary card, and the footer. Validation mirrors the backend's rules in
// internal/deploy/params.go; the server re-validates everything.
import { create } from "zustand";

import type { CreateGuestRequest } from "@/lib/api/generated/types";

export type WizardKind = "qemu" | "lxc";
export type SourceMode = "iso" | "vztmpl" | "clone";

export const TAB_NAMES = ["Basics", "Image", "Size", "Networking", "Advanced", "Tags", "Review + create"] as const;

export const SIZE_PRESETS = [
  { name: "S", cores: 2, ramGiB: 4 },
  { name: "M", cores: 4, ramGiB: 8 },
  { name: "L", cores: 8, ramGiB: 16 },
  { name: "XL", cores: 16, ramGiB: 32 },
] as const;

const NAME_RE = /^[a-z][a-z0-9-]{0,39}$/;
const TAG_RE = /^[a-z0-9_][a-z0-9_.-]*$/;
const CIDR_RE = /^\d{1,3}(\.\d{1,3}){3}\/\d{1,2}$/;
const IP_RE = /^\d{1,3}(\.\d{1,3}){3}$/;

export interface WizardState {
  kind: WizardKind;
  tab: number;
  maxTab: number;

  // Basics
  name: string;
  node: string;
  vmid: string; // keep as string for the input; validated to int
  projectId: string; // required (Phase 3): the backend derives the pool from it
  projectName: string; // display-only mirror for the summary/review panels

  // Image
  sourceMode: SourceMode;
  isoVolId: string;
  vztmplVolId: string;
  cloneVmid: number | null;
  cloneMode: "full" | "linked";

  // Size
  cores: string;
  memoryMb: string;
  diskGb: string;
  storage: string;

  // Networking
  bridge: string;
  vlanTag: string;
  firewall: boolean;
  ipMode: "dhcp" | "static";
  cidr: string;
  gateway: string;

  // Advanced
  ciUser: string;
  ciPassword: string;
  sshKeys: string; // textarea, one key per line
  nameserver: string;

  // Tags
  tags: string[];

  startAfterCreate: boolean;

  // actions
  init(kind: WizardKind): void;
  set(patch: Partial<WizardState>): void;
  goTab(i: number): void;
  next(): void;
  prev(): void;
}

const initial = (kind: WizardKind) => ({
  kind,
  tab: 0,
  maxTab: 0,
  name: "",
  node: "",
  vmid: "",
  projectId: "",
  projectName: "",
  sourceMode: (kind === "lxc" ? "vztmpl" : "iso") as SourceMode,
  isoVolId: "",
  vztmplVolId: "",
  cloneVmid: null,
  cloneMode: "full" as const,
  cores: "2",
  memoryMb: "2048",
  diskGb: kind === "lxc" ? "8" : "32",
  storage: "",
  bridge: "",
  vlanTag: "",
  firewall: false,
  ipMode: "dhcp" as const,
  cidr: "",
  gateway: "",
  ciUser: "",
  ciPassword: "",
  sshKeys: "",
  nameserver: "",
  tags: [],
  startAfterCreate: true,
});

export const useWizardStore = create<WizardState>((set, get) => ({
  ...initial("qemu"),
  init: (kind) => set({ ...initial(kind) }),
  set: (patch) => set(patch),
  goTab: (i) => {
    if (i <= get().maxTab) set({ tab: i });
  },
  next: () => {
    const { tab, maxTab } = get();
    if (tab < TAB_NAMES.length - 1) set({ tab: tab + 1, maxTab: Math.max(maxTab, tab + 1) });
  },
  prev: () => {
    const { tab } = get();
    if (tab > 0) set({ tab: tab - 1 });
  },
}));

export interface WizardError {
  tab: number;
  field: string;
  message: string;
}

/** Full validation across tabs; mirrors backend deploy.Validate. */
export function validateWizard(s: WizardState, existingVmids: number[] = []): WizardError[] {
  const errs: WizardError[] = [];
  const kindLabel = s.kind === "qemu" ? "Virtual machine" : "Container";

  if (s.name === "") {
    errs.push({ tab: 0, field: "name", message: `${kindLabel} name is required (Basics).` });
  } else if (!NAME_RE.test(s.name)) {
    errs.push({
      tab: 0,
      field: "name",
      message: "Name must start with a lowercase letter and contain only lowercase letters, digits, and hyphens (Basics).",
    });
  }
  if (s.node === "") errs.push({ tab: 0, field: "node", message: "Target node is required (Basics)." });
  if (s.projectId === "") errs.push({ tab: 0, field: "projectId", message: "A project is required (Basics)." });

  const vmid = Number(s.vmid);
  if (!Number.isInteger(vmid) || vmid < 100 || vmid > 999999999) {
    errs.push({ tab: 0, field: "vmid", message: "VMID must be an integer between 100 and 999999999 (Basics)." });
  } else if (existingVmids.includes(vmid)) {
    errs.push({ tab: 0, field: "vmid", message: `VMID ${vmid} is already in use (Basics).` });
  }

  if (s.kind === "lxc") {
    if (s.vztmplVolId === "") errs.push({ tab: 1, field: "vztmplVolId", message: "A container template is required (Image)." });
  } else if (s.sourceMode === "iso") {
    if (s.isoVolId === "") errs.push({ tab: 1, field: "isoVolId", message: "An ISO image is required (Image)." });
  } else if (s.sourceMode === "clone") {
    if (!s.cloneVmid) errs.push({ tab: 1, field: "cloneVmid", message: "A template to clone is required (Image)." });
  }

  const cores = Number(s.cores);
  if (!Number.isInteger(cores) || cores < 1 || cores > 128) {
    errs.push({ tab: 2, field: "cores", message: "Cores must be between 1 and 128 (Size)." });
  }
  const mem = Number(s.memoryMb);
  if (!Number.isInteger(mem) || mem < 128) {
    errs.push({ tab: 2, field: "memoryMb", message: "Memory must be at least 128 MiB (Size)." });
  }
  if (s.sourceMode !== "clone") {
    const disk = Number(s.diskGb);
    if (!Number.isInteger(disk) || disk < 1) {
      errs.push({ tab: 2, field: "diskGb", message: "Disk size must be at least 1 GiB (Size)." });
    }
    if (s.storage === "") errs.push({ tab: 2, field: "storage", message: "A storage pool is required (Size)." });
    if (s.bridge === "") errs.push({ tab: 3, field: "bridge", message: "A network bridge is required (Networking)." });
  }

  if (s.vlanTag !== "") {
    const tag = Number(s.vlanTag);
    if (!Number.isInteger(tag) || tag < 1 || tag > 4094) {
      errs.push({ tab: 3, field: "vlanTag", message: "VLAN tag must be between 1 and 4094 (Networking)." });
    }
  }
  if (s.ipMode === "static") {
    if (!CIDR_RE.test(s.cidr)) {
      errs.push({ tab: 3, field: "cidr", message: "Static IP must be CIDR notation, e.g. 192.168.1.50/24 (Networking)." });
    }
    if (s.gateway !== "" && !IP_RE.test(s.gateway)) {
      errs.push({ tab: 3, field: "gateway", message: "Gateway must be an IPv4 address (Networking)." });
    }
  }

  for (const t of s.tags) {
    if (!TAG_RE.test(t)) {
      errs.push({ tab: 5, field: "tags", message: `Invalid tag "${t}" — lowercase letters, digits, . - _ only (Tags).` });
    }
  }
  return errs;
}

/** Wire request for POST /api/tenants/{tenantId}/guests. Call only when validation passes. */
export function toCreateRequest(s: WizardState): CreateGuestRequest {
  const sshKeys = s.sshKeys
    .split("\n")
    .map((k) => k.trim())
    .filter(Boolean);
  const hasCI = s.ciUser !== "" || s.ciPassword !== "" || sshKeys.length > 0 || s.nameserver !== "";

  return {
    type: s.kind,
    name: s.name,
    node: s.node,
    vmid: Number(s.vmid),
    projectId: s.projectId,
    source:
      s.kind === "lxc"
        ? { mode: "vztmpl", vztmplVolId: s.vztmplVolId }
        : s.sourceMode === "clone"
          ? { mode: "clone", cloneVmid: s.cloneVmid ?? 0, cloneMode: s.cloneMode }
          : { mode: "iso", isoVolId: s.isoVolId },
    cores: Number(s.cores),
    memoryMb: Number(s.memoryMb),
    diskGb: s.sourceMode === "clone" ? 0 : Number(s.diskGb),
    storage: s.sourceMode === "clone" ? (s.cloneMode === "full" ? s.storage : "") : s.storage,
    bridge: s.sourceMode === "clone" ? "" : s.bridge,
    ...(s.vlanTag !== "" ? { vlanTag: Number(s.vlanTag) } : {}),
    firewall: s.firewall,
    ...(s.ipMode === "static" || s.kind === "lxc" || hasCI
      ? {
          ipConfig:
            s.ipMode === "static"
              ? { mode: "static", cidr: s.cidr, ...(s.gateway !== "" ? { gateway: s.gateway } : {}) }
              : { mode: "dhcp" },
        }
      : {}),
    ...(hasCI
      ? {
          cloudInit: {
            ...(s.ciUser !== "" ? { user: s.ciUser } : {}),
            ...(s.ciPassword !== "" ? { password: s.ciPassword } : {}),
            ...(sshKeys.length > 0 ? { sshKeys } : {}),
            ...(s.nameserver !== "" ? { nameserver: s.nameserver } : {}),
          },
        }
      : {}),
    ...(s.tags.length > 0 ? { tags: s.tags } : {}),
    startAfterCreate: s.startAfterCreate,
  };
}
