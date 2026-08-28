"use client";
// Create-wizard state — zustand store shared by all seven tabs, the sticky
// summary card, and the footer. Validation mirrors the backend's rules in
// internal/deploy/params.go; the server re-validates everything.
import { create } from "zustand";

import type {
  CreateGuestRequest,
  ProjectQuotaResponse,
  ProvisionServiceRequest,
} from "@/lib/api/generated/types";

export type WizardKind = "qemu" | "lxc";
export type SourceMode = "iso" | "vztmpl" | "clone";

export const TAB_NAMES = [
  "Basics",
  "Image",
  "Size",
  "Networking",
  "Advanced",
  "Tags",
  "Review + create",
] as const;

/**
 * Semantic keys for the named tabs the wizard code navigates to by intent
 * (jump-to-Size on an over-quota bounce, jump-to-Review on submit). Resolving
 * them through TAB_NAMES means no code hardcodes a positional index — inserting
 * a step (e.g. the Phase-C Credentials tab) shifts every reference automatically
 * instead of silently pointing at the wrong tab.
 */
export type TabKey = "basics" | "image" | "size" | "networking" | "advanced" | "tags" | "review";

const TAB_KEY_TO_LABEL: Record<TabKey, (typeof TAB_NAMES)[number]> = {
  basics: "Basics",
  image: "Image",
  size: "Size",
  networking: "Networking",
  advanced: "Advanced",
  tags: "Tags",
  review: "Review + create",
};

/** Position of a named tab in TAB_NAMES (single source of truth for ordering). */
export function tabIndex(key: TabKey): number {
  return TAB_NAMES.indexOf(TAB_KEY_TO_LABEL[key]);
}

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

  // Service-catalog mode (ADR-0026). Empty for a bare VM/LXC. When set, the
  // wizard provisions the named service: the base image is defined by the
  // service (no Image picker), and submit posts a ProvisionServiceRequest — the
  // superuser credential is generated server-side and shown once after create.
  serviceId: string;
  serviceName: string; // display-only mirror for the header/summary

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
  serviceId: "",
  serviceName: "",
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

/**
 * Effective remaining quota per dimension for the picked project. null = no cap
 * on that dimension (unlimited). Wire units: ramMb is MiB, diskGb is GiB — the
 * same units the wizard's memoryMb/diskGb fields use, so comparisons are direct.
 */
export interface QuotaRemaining {
  vcpu: number | null;
  ramMb: number | null;
  diskGb: number | null;
  count: number | null;
}

/**
 * The wizard must respect the tighter of project vs tenant remaining. Remaining
 * on a dimension is only meaningful where that scope actually sets a limit
 * (QuotaWithUsage.remaining is 0 elsewhere), so we min only over scopes that
 * have a non-null limit; if neither caps the dimension it stays unlimited.
 */
export function effectiveRemaining(resp: ProjectQuotaResponse): QuotaRemaining {
  const dim = (
    projHas: boolean,
    projRem: number,
    tenHas: boolean,
    tenRem: number,
  ): number | null => {
    const vals: number[] = [];
    if (projHas) vals.push(projRem);
    if (tenHas) vals.push(tenRem);
    return vals.length === 0 ? null : Math.min(...vals);
  };
  const p = resp.project;
  const t = resp.tenant;
  return {
    vcpu: dim(
      p.limits.maxVcpu != null,
      p.remaining.vcpu,
      t.limits.maxVcpu != null,
      t.remaining.vcpu,
    ),
    ramMb: dim(
      p.limits.maxRamMb != null,
      p.remaining.ramMb,
      t.limits.maxRamMb != null,
      t.remaining.ramMb,
    ),
    diskGb: dim(
      p.limits.maxDiskGb != null,
      p.remaining.diskGb,
      t.limits.maxDiskGb != null,
      t.remaining.diskGb,
    ),
    count: dim(
      p.limits.maxCount != null,
      p.remaining.count,
      t.limits.maxCount != null,
      t.remaining.count,
    ),
  };
}

/**
 * Full validation across tabs; mirrors backend deploy.Validate. `remaining`, when
 * supplied, adds fast client-side over-quota checks on the Size tab (the backend
 * reservation still enforces authoritatively — this is just early feedback).
 */
export function validateWizard(
  s: WizardState,
  existingVmids: number[] = [],
  remaining: QuotaRemaining | null = null,
): WizardError[] {
  const errs: WizardError[] = [];
  const kindLabel = s.kind === "qemu" ? "Virtual machine" : "Container";

  if (s.name === "") {
    errs.push({ tab: 0, field: "name", message: `${kindLabel} name is required (Basics).` });
  } else if (!NAME_RE.test(s.name)) {
    errs.push({
      tab: 0,
      field: "name",
      message:
        "Name must start with a lowercase letter and contain only lowercase letters, digits, and hyphens (Basics).",
    });
  }
  if (s.node === "")
    errs.push({ tab: 0, field: "node", message: "Target node is required (Basics)." });
  if (s.projectId === "")
    errs.push({ tab: 0, field: "projectId", message: "A project is required (Basics)." });

  const vmid = Number(s.vmid);
  if (!Number.isInteger(vmid) || vmid < 100 || vmid > 999999999) {
    errs.push({
      tab: 0,
      field: "vmid",
      message: "VMID must be an integer between 100 and 999999999 (Basics).",
    });
  } else if (existingVmids.includes(vmid)) {
    errs.push({ tab: 0, field: "vmid", message: `VMID ${vmid} is already in use (Basics).` });
  }

  if (s.kind === "lxc") {
    if (s.vztmplVolId === "")
      errs.push({
        tab: 1,
        field: "vztmplVolId",
        message: "A container template is required (Image).",
      });
  } else if (s.sourceMode === "iso") {
    // A catalog service supplies its own base image server-side, so the Image
    // tab has no ISO to pick — skip the requirement in service mode.
    if (s.serviceId === "" && s.isoVolId === "")
      errs.push({ tab: 1, field: "isoVolId", message: "An ISO image is required (Image)." });
  } else if (s.sourceMode === "clone") {
    if (!s.cloneVmid)
      errs.push({
        tab: 1,
        field: "cloneVmid",
        message: "A template to clone is required (Image).",
      });
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
    if (s.storage === "")
      errs.push({ tab: 2, field: "storage", message: "A storage pool is required (Size)." });
    if (s.bridge === "")
      errs.push({ tab: 3, field: "bridge", message: "A network bridge is required (Networking)." });
  }

  if (s.vlanTag !== "") {
    const tag = Number(s.vlanTag);
    if (!Number.isInteger(tag) || tag < 1 || tag > 4094) {
      errs.push({
        tab: 3,
        field: "vlanTag",
        message: "VLAN tag must be between 1 and 4094 (Networking).",
      });
    }
  }
  if (s.ipMode === "static") {
    if (!CIDR_RE.test(s.cidr)) {
      errs.push({
        tab: 3,
        field: "cidr",
        message: "Static IP must be CIDR notation, e.g. 192.168.1.50/24 (Networking).",
      });
    }
    if (s.gateway !== "" && !IP_RE.test(s.gateway)) {
      errs.push({
        tab: 3,
        field: "gateway",
        message: "Gateway must be an IPv4 address (Networking).",
      });
    }
  }

  for (const t of s.tags) {
    if (!TAG_RE.test(t)) {
      errs.push({
        tab: 5,
        field: "tags",
        message: `Invalid tag "${t}" — lowercase letters, digits, . - _ only (Tags).`,
      });
    }
  }

  // Over-quota checks (Size tab) — client-side fast feedback; the backend
  // reservation is authoritative. Each dimension is only checked where the
  // project/tenant actually sets a cap (remaining non-null).
  if (remaining) {
    if (remaining.count != null && remaining.count < 1) {
      errs.push({
        tab: 2,
        field: "quota",
        message:
          "This project's guest-count quota is exhausted — free a guest or raise the quota (Size).",
      });
    }
    // For clones the reservation delta is the SOURCE template's allocation
    // (server-authoritative), not the wizard's cores/memory/disk — so skip the
    // per-dimension client checks for clones and let the server's 409 speak.
    // (The count check above still applies: a clone adds one guest.)
    if (s.sourceMode !== "clone") {
      const rc = Number(s.cores);
      if (remaining.vcpu != null && Number.isInteger(rc) && rc > remaining.vcpu) {
        errs.push({
          tab: 2,
          field: "cores",
          message: `Only ${remaining.vcpu} vCPU remaining in this project's quota (Size).`,
        });
      }
      const rm = Number(s.memoryMb);
      if (remaining.ramMb != null && Number.isInteger(rm) && rm > remaining.ramMb) {
        errs.push({
          tab: 2,
          field: "memoryMb",
          message: `Only ${remaining.ramMb} MiB of memory remaining in this project's quota (Size).`,
        });
      }
      const rd = Number(s.diskGb);
      if (remaining.diskGb != null && Number.isInteger(rd) && rd > remaining.diskGb) {
        errs.push({
          tab: 2,
          field: "diskGb",
          message: `Only ${remaining.diskGb} GiB of disk remaining in this project's quota (Size).`,
        });
      }
    }
  }

  // Catalog services lock the password and set cicustom (which drops cipassword),
  // so an SSH key is the ONLY way into the guest — require at least one. The
  // server rejects an empty sshKeys with a 400; mirror that here (Advanced tab).
  if (s.serviceId !== "" && s.sshKeys.split("\n").every((k) => k.trim() === "")) {
    errs.push({
      tab: tabIndex("advanced"),
      field: "sshKeys",
      message: "An SSH public key is required for catalog services (Advanced).",
    });
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
              ? {
                  mode: "static",
                  cidr: s.cidr,
                  ...(s.gateway !== "" ? { gateway: s.gateway } : {}),
                }
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

/**
 * Wire request for POST /api/tenants/{tenantId}/service-catalog/{serviceId}/provision
 * (service mode only). The service defines its own base image and generates the
 * superuser credential server-side, so this carries no source and no cloud-init
 * account fields — only sizing, placement, network, SSH keys, and tags. Call
 * only when validation passes and s.serviceId is set.
 */
export function toProvisionRequest(s: WizardState): ProvisionServiceRequest {
  const sshKeys = s.sshKeys
    .split("\n")
    .map((k) => k.trim())
    .filter(Boolean);

  return {
    projectId: s.projectId,
    name: s.name,
    node: s.node,
    vmid: Number(s.vmid),
    cores: Number(s.cores),
    memoryMb: Number(s.memoryMb),
    diskGb: Number(s.diskGb),
    storage: s.storage,
    bridge: s.bridge,
    ...(s.vlanTag !== "" ? { vlanTag: Number(s.vlanTag) } : {}),
    firewall: s.firewall,
    ipConfig:
      s.ipMode === "static"
        ? { mode: "static", cidr: s.cidr, ...(s.gateway !== "" ? { gateway: s.gateway } : {}) }
        : { mode: "dhcp" },
    ...(sshKeys.length > 0 ? { sshKeys } : {}),
    ...(s.tags.length > 0 ? { tags: s.tags } : {}),
  };
}
