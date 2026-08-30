"use client";
// Create-wizard state — zustand store shared by all seven tabs, the sticky
// summary card, and the footer. Validation mirrors the backend's rules in
// internal/deploy/params.go; the server re-validates everything.
import { create } from "zustand";

import type {
  CatalogCredential,
  CreateGuestRequest,
  ProjectQuotaResponse,
  ProvisionServiceRequest,
} from "@/lib/api/generated/types";

export type WizardKind = "qemu" | "lxc";
export type SourceMode = "iso" | "vztmpl" | "clone";

/**
 * Semantic keys for every wizard step the code navigates to by intent
 * (jump-to-Size on an over-quota bounce, jump-to-Review on submit). Resolving a
 * key to a position through the CURRENT step list (never a hardcoded literal)
 * means inserting the Phase-C Credentials step shifts every reference
 * automatically instead of silently pointing at the wrong tab.
 */
export type StepKey =
  "basics" | "image" | "size" | "networking" | "advanced" | "credentials" | "tags" | "review";

/** Human labels per step — the single source of truth for the tab strip. */
export const STEP_LABEL: Record<StepKey, string> = {
  basics: "Basics",
  image: "Image",
  size: "Size",
  networking: "Networking",
  advanced: "Advanced",
  credentials: "Credentials",
  tags: "Tags",
  review: "Review + create",
};

/** The bare VM/LXC flow — seven steps, no Credentials. */
const BASE_STEPS: StepKey[] = [
  "basics",
  "image",
  "size",
  "networking",
  "advanced",
  "tags",
  "review",
];

/**
 * The bare-flow labels, kept as a named export for the tab strip and any code
 * that iterates the default order. Derived from BASE_STEPS so labels never drift.
 */
export const TAB_NAMES: readonly string[] = BASE_STEPS.map((k) => STEP_LABEL[k]);

/**
 * Ordered steps for the CURRENT wizard mode. Plain creates get BASE_STEPS
 * unchanged; a catalog service that declares credentials inserts a Credentials
 * step after Advanced. The list — not a static index map — is the single source
 * of truth for ordering, so every positional reference is derived (stepIndex /
 * tabIndex) and inserting a step never points code at the wrong tab. A plain
 * VM/LXC create is byte-identical to before (BASE_STEPS).
 */
export function wizardSteps(
  s: Pick<WizardState, "serviceId" | "serviceHasCredentials">,
): StepKey[] {
  if (s.serviceId !== "" && s.serviceHasCredentials) {
    return ["basics", "image", "size", "networking", "advanced", "credentials", "tags", "review"];
  }
  return BASE_STEPS;
}

/** Position of a named step within a given (mode-aware) step list. */
export function stepIndex(steps: StepKey[], key: StepKey): number {
  return steps.indexOf(key);
}

/**
 * Position of a named step in the BARE flow. Retained for plain-create callers
 * and existing tests; mode-aware code resolves against wizardSteps(...) via
 * stepIndex. Never hardcode a positional index — resolve through these.
 */
export function tabIndex(key: StepKey): number {
  return stepIndex(BASE_STEPS, key);
}

/** Server-authoritative floor mirrored client-side for UX (ADR-0027 §3). */
export const MIN_CREDENTIAL_PASSWORD_LEN = 12;

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

/**
 * One credential row in the wizard: the service's declared schema flags
 * (denormalised from CatalogCredential so toProvisionRequest and validation stay
 * pure functions of state) plus the user's generate-vs-set choice. A password is
 * only carried when the user chose to SET it; it is never surfaced back after
 * submit (ADR-0028).
 */
export interface WizardCredential {
  /** Credential name from the service schema — the key the server matches on. */
  name: string;
  /** Whether the user may customise the username (else it is fixed server-side). */
  usernameSettable: boolean;
  /** Whether the user may SET this credential at all (else always generated). */
  userSettable: boolean;
  /** The service's fixed/default username, shown read-only when !usernameSettable. */
  fixedUsername: string;
  /** User choice: generate server-side (default) or supply a value. */
  mode: "generate" | "set";
  /** User-entered username (meaningful only when usernameSettable && mode="set"). */
  username: string;
  /** User-entered password (meaningful only when mode="set"). */
  password: string;
}

/**
 * Build wizard credential rows from a service's declared credential schema. Every
 * credential defaults to "generate" (server-side crypto/rand) per Phase C.
 */
export function makeWizardCredentials(schema: CatalogCredential[]): WizardCredential[] {
  return schema.map((c) => ({
    name: c.name,
    usernameSettable: c.usernameSettable,
    userSettable: c.userSettable,
    fixedUsername: c.username ?? "",
    mode: "generate" as const,
    username: c.username ?? "",
    password: "",
  }));
}

export interface WizardState {
  kind: WizardKind;
  tab: number;
  maxTab: number;

  // Service-catalog mode (ADR-0026). Empty for a bare VM/LXC. When set, the
  // wizard provisions the named service: the base image is defined by the
  // service (no Image picker), and submit posts a ProvisionServiceRequest — each
  // service credential is either generated server-side (default, shown once) or
  // set by the user on the Credentials step (Phase C).
  serviceId: string;
  serviceName: string; // display-only mirror for the header/summary
  // True when the selected service declares credentials[] — gates the extra
  // Credentials step in wizardSteps(). Populated from the loaded service def.
  serviceHasCredentials: boolean;
  // Per-credential generate-vs-set choices, populated from the service schema.
  credentials: WizardCredential[];

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
  serviceHasCredentials: false,
  credentials: [] as WizardCredential[],
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
    const st = get();
    const count = wizardSteps(st).length;
    if (st.tab < count - 1) set({ tab: st.tab + 1, maxTab: Math.max(st.maxTab, st.tab + 1) });
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
  // Resolve every error's tab against the CURRENT (mode-aware) step list, so a
  // Credentials step inserted in service mode shifts the trailing tabs' indices
  // correctly. For the bare flow these collapse to the historical 0..5.
  const steps = wizardSteps(s);
  const at = (key: StepKey) => stepIndex(steps, key);

  if (s.name === "") {
    errs.push({
      tab: at("basics"),
      field: "name",
      message: `${kindLabel} name is required (Basics).`,
    });
  } else if (!NAME_RE.test(s.name)) {
    errs.push({
      tab: at("basics"),
      field: "name",
      message:
        "Name must start with a lowercase letter and contain only lowercase letters, digits, and hyphens (Basics).",
    });
  }
  if (s.node === "")
    errs.push({ tab: at("basics"), field: "node", message: "Target node is required (Basics)." });
  if (s.projectId === "")
    errs.push({
      tab: at("basics"),
      field: "projectId",
      message: "A project is required (Basics).",
    });

  const vmid = Number(s.vmid);
  if (!Number.isInteger(vmid) || vmid < 100 || vmid > 999999999) {
    errs.push({
      tab: at("basics"),
      field: "vmid",
      message: "VMID must be an integer between 100 and 999999999 (Basics).",
    });
  } else if (existingVmids.includes(vmid)) {
    errs.push({
      tab: at("basics"),
      field: "vmid",
      message: `VMID ${vmid} is already in use (Basics).`,
    });
  }

  if (s.kind === "lxc") {
    if (s.vztmplVolId === "")
      errs.push({
        tab: at("image"),
        field: "vztmplVolId",
        message: "A container template is required (Image).",
      });
  } else if (s.sourceMode === "iso") {
    // A catalog service supplies its own base image server-side, so the Image
    // tab has no ISO to pick — skip the requirement in service mode.
    if (s.serviceId === "" && s.isoVolId === "")
      errs.push({
        tab: at("image"),
        field: "isoVolId",
        message: "An ISO image is required (Image).",
      });
  } else if (s.sourceMode === "clone") {
    if (!s.cloneVmid)
      errs.push({
        tab: at("image"),
        field: "cloneVmid",
        message: "A template to clone is required (Image).",
      });
  }

  const cores = Number(s.cores);
  if (!Number.isInteger(cores) || cores < 1 || cores > 128) {
    errs.push({
      tab: at("size"),
      field: "cores",
      message: "Cores must be between 1 and 128 (Size).",
    });
  }
  const mem = Number(s.memoryMb);
  if (!Number.isInteger(mem) || mem < 128) {
    errs.push({
      tab: at("size"),
      field: "memoryMb",
      message: "Memory must be at least 128 MiB (Size).",
    });
  }
  if (s.sourceMode !== "clone") {
    const disk = Number(s.diskGb);
    if (!Number.isInteger(disk) || disk < 1) {
      errs.push({
        tab: at("size"),
        field: "diskGb",
        message: "Disk size must be at least 1 GiB (Size).",
      });
    }
    if (s.storage === "")
      errs.push({
        tab: at("size"),
        field: "storage",
        message: "A storage pool is required (Size).",
      });
    if (s.bridge === "")
      errs.push({
        tab: at("networking"),
        field: "bridge",
        message: "A network bridge is required (Networking).",
      });
  }

  if (s.vlanTag !== "") {
    const tag = Number(s.vlanTag);
    if (!Number.isInteger(tag) || tag < 1 || tag > 4094) {
      errs.push({
        tab: at("networking"),
        field: "vlanTag",
        message: "VLAN tag must be between 1 and 4094 (Networking).",
      });
    }
  }
  if (s.ipMode === "static") {
    if (!CIDR_RE.test(s.cidr)) {
      errs.push({
        tab: at("networking"),
        field: "cidr",
        message: "Static IP must be CIDR notation, e.g. 192.168.1.50/24 (Networking).",
      });
    }
    if (s.gateway !== "" && !IP_RE.test(s.gateway)) {
      errs.push({
        tab: at("networking"),
        field: "gateway",
        message: "Gateway must be an IPv4 address (Networking).",
      });
    }
  }

  for (const t of s.tags) {
    if (!TAG_RE.test(t)) {
      errs.push({
        tab: at("tags"),
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
      tab: at("advanced"),
      field: "sshKeys",
      message: "An SSH public key is required for catalog services (Advanced).",
    });
  }

  // Phase-C credentials: for each credential the user chose to SET, mirror the
  // server's length-only password policy (≥ 12 chars; metacharacters allowed —
  // NO composition rules). Generated credentials carry nothing to validate. A
  // supplied username is only sent when usernameSettable, so it needs no rule
  // here. Errors are keyed per credential so the tab shows them inline.
  if (s.serviceId !== "") {
    for (const c of s.credentials) {
      if (c.mode !== "set") continue;
      if (c.password.length < MIN_CREDENTIAL_PASSWORD_LEN) {
        errs.push({
          tab: at("credentials"),
          field: `credential:${c.name}:password`,
          message: `Password must be at least ${MIN_CREDENTIAL_PASSWORD_LEN} characters (Credentials).`,
        });
      }
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

  // Include ONLY the credentials the user chose to SET; omit generated ones so
  // the server falls back to crypto/rand (Phase A). Send `username` only when the
  // credential is usernameSettable — a fixed-username credential (e.g. Postgres
  // `postgres`) 400s if a username is sent — and only when non-empty (blank means
  // "use the default"). A supplied password is passed verbatim; the server
  // enforces the ≥ 12 length policy authoritatively.
  const credentials = s.credentials
    .filter((c) => c.mode === "set")
    .map((c) => ({
      name: c.name,
      ...(c.usernameSettable && c.username.trim() !== "" ? { username: c.username } : {}),
      password: c.password,
    }));

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
    ...(credentials.length > 0 ? { credentials } : {}),
  };
}
