// Deployment-set create-form logic (Phase E, ADR-0029/0030). A set is
// structurally different from the single-guest wizard — it provisions N linked
// members (one K3s `server` control plane + `agent` workers) sharing a
// lifecycle — so it gets a dedicated, pure form module rather than the zustand
// wizard store. Every rule here mirrors the backend's buildSetProvision +
// deploy.Validate (internal/handlers/deployment_sets.go); the server re-validates
// authoritatively. These functions are pure so they can be unit-tested directly.

import type { CatalogRole, CatalogService, CreateSetRequest } from "@/lib/api/generated/types";

// Client-side mirrors of the backend guest/name/network rules (kept local to
// this module — the server is the single source of truth and re-checks each).
const NAME_RE = /^[a-z][a-z0-9-]{0,39}$/;
const TAG_RE = /^[a-z0-9_][a-z0-9_.-]*$/;
const CIDR_RE = /^\d{1,3}(\.\d{1,3}){3}\/\d{1,2}$/;
const IP_RE = /^\d{1,3}(\.\d{1,3}){3}$/;

/** Lowest / highest valid Proxmox VMID (mirrors validateWizard). */
export const VMID_MIN = 100;
export const VMID_MAX = 999999999;

/** Resolved worker-count bounds for a set service's `agent` role. */
export interface AgentBounds {
  min: number;
  max: number;
  default: number;
}

/** The dedicated set-create form state (dedicated route, not the wizard store). */
export interface SetFormState {
  serviceId: string;
  name: string;
  projectId: string;
  projectName: string; // display-only mirror for the summary
  node: string;
  storage: string;
  bridge: string;
  vlanTag: string; // kept as string for the input; validated to int
  firewall: boolean;
  agentCount: number;
  /** First VMID of the block; server takes it, agents take the next free ids. */
  startVmid: string;
  cidr: string; // server static control-plane IP, e.g. 192.168.1.50/24
  gateway: string; // server gateway, e.g. 192.168.1.1
  sshKeys: string; // textarea, one key per line
  tags: string[];
}

/** One field-keyed validation error for inline display. */
export interface SetFormError {
  field: string;
  message: string;
}

/** Sum of every member's reserved allocation — for inline quota feedback. */
export interface SetTotals {
  vcpu: number;
  ramMb: number;
  diskGb: number;
  count: number;
}

export function emptySetForm(serviceId: string): SetFormState {
  return {
    serviceId,
    name: "",
    projectId: "",
    projectName: "",
    node: "",
    storage: "",
    bridge: "",
    vlanTag: "",
    firewall: false,
    agentCount: 0,
    startVmid: "",
    cidr: "",
    gateway: "",
    sshKeys: "",
    tags: [],
  };
}

/** Locate a role definition by name (case-sensitive, matching the backend). */
export function findRole(svc: CatalogService, name: string): CatalogRole | undefined {
  return (svc.roles ?? []).find((r) => r.name === name);
}

/**
 * Resolve the agent role's worker-count bounds, mirroring buildSetProvision's
 * zero-fallbacks exactly: a zero min/max collapses to the role's default count,
 * and a zero default is treated as 1 (a set needs at least one worker to differ
 * from a single guest — the backend's role config supplies a real default).
 */
export function agentBounds(agent: CatalogRole | undefined): AgentBounds {
  const def = agent?.count && agent.count > 0 ? agent.count : 1;
  const min = agent?.min && agent.min > 0 ? agent.min : def;
  const max = agent?.max && agent.max > 0 ? agent.max : def;
  return { min, max, default: def };
}

/**
 * Clamp a requested worker count into [min,max]; a zero/undefined request falls
 * back to the role default (mirrors buildSetProvision: `if count == 0 { count =
 * agent.Count }` then the [min,max] bound).
 */
export function clampAgentCount(count: number, bounds: AgentBounds): number {
  const c = !count || count <= 0 ? bounds.default : Math.floor(count);
  if (c < bounds.min) return bounds.min;
  if (c > bounds.max) return bounds.max;
  return c;
}

/**
 * Allocate `agentCount + 1` distinct free VMIDs starting at `startVmid`: the
 * first is the control-plane server, the rest are the agents (len === agentCount,
 * the invariant CreateSetRequest requires). `taken` (the tenant's known-in-use
 * VMIDs) is skipped for a good first guess — the server's atomic
 * ReserveOwnershipBatch is authoritative and 409s on a real collision, which the
 * form routes back inline.
 */
export function allocateSetVmids(
  startVmid: number,
  agentCount: number,
  taken: ReadonlySet<number> = new Set(),
): { serverVmid: number; agentVmids: number[] } {
  const need = Math.max(agentCount, 0) + 1;
  const ids: number[] = [];
  // Bound the scan so a pathological `taken` set can never loop unbounded.
  const guard = startVmid + need + 100000;
  for (let v = startVmid; v <= guard && ids.length < need; v++) {
    if (!taken.has(v)) ids.push(v);
  }
  // Degenerate fallback (should never trigger for sane inputs): pad sequentially.
  while (ids.length < need) ids.push((ids[ids.length - 1] ?? startVmid) + 1);
  return { serverVmid: ids[0], agentVmids: ids.slice(1) };
}

/** Split + trim the SSH-key textarea into individual keys (drops blank lines). */
export function parseSshKeys(raw: string): string[] {
  return raw
    .split("\n")
    .map((k) => k.trim())
    .filter(Boolean);
}

/** Sum of the server plus `agentCount` agents' default sizing (for quota UX). */
export function setTotals(svc: CatalogService, agentCount: number): SetTotals {
  const server = findRole(svc, "server");
  const agent = findRole(svc, "agent");
  const sd = server?.sizing.default;
  const ad = agent?.sizing.default;
  const n = Math.max(agentCount, 0);
  return {
    vcpu: (sd?.cores ?? 0) + n * (ad?.cores ?? 0),
    ramMb: (sd?.memoryMb ?? 0) + n * (ad?.memoryMb ?? 0),
    diskGb: (sd?.diskGb ?? 0) + n * (ad?.diskGb ?? 0),
    count: n + 1,
  };
}

/**
 * Full client-side validation of the set form. Mirrors buildSetProvision +
 * deploy.Validate: name/member-name rules, required placement, a STATIC
 * control-plane serverIp (CIDR + gateway), at least one SSH key, the agent-count
 * bounds, and distinct in-range VMIDs. `taken` adds a best-effort "already in
 * use" check; the server's atomic reservation is authoritative.
 */
export function validateSetForm(
  s: SetFormState,
  bounds: AgentBounds,
  taken: ReadonlySet<number> = new Set(),
): SetFormError[] {
  const errs: SetFormError[] = [];
  const count = clampAgentCount(s.agentCount, bounds);

  // Name — the base becomes <name>-server / <name>-agent-N guest hostnames, so
  // validate the LONGEST assembled member name too (the server 400s if a suffix
  // pushes a member name past the 40-char guest-name limit).
  if (s.name === "") {
    errs.push({ field: "name", message: "A cluster name is required." });
  } else if (!NAME_RE.test(s.name)) {
    errs.push({
      field: "name",
      message:
        "Name must start with a lowercase letter and contain only lowercase letters, digits, and hyphens.",
    });
  } else if (!NAME_RE.test(`${s.name}-agent-${Math.max(count, 1)}`)) {
    errs.push({
      field: "name",
      message: "Name is too long once the member suffixes (…-server, …-agent-N) are added.",
    });
  }

  if (s.projectId === "") errs.push({ field: "projectId", message: "A project is required." });
  if (s.node === "") errs.push({ field: "node", message: "A target node is required." });
  if (s.storage === "")
    errs.push({ field: "storage", message: "A storage pool for the member disks is required." });
  if (s.bridge === "") errs.push({ field: "bridge", message: "A network bridge is required." });

  if (s.vlanTag !== "") {
    const tag = Number(s.vlanTag);
    if (!Number.isInteger(tag) || tag < 1 || tag > 4094)
      errs.push({ field: "vlanTag", message: "VLAN tag must be between 1 and 4094." });
  }

  // Worker count within the service role bounds.
  if (!Number.isInteger(s.agentCount) || s.agentCount < bounds.min || s.agentCount > bounds.max) {
    errs.push({
      field: "agentCount",
      message: `Worker count must be between ${bounds.min} and ${bounds.max}.`,
    });
  }

  // Static control-plane IP (ADR-0030): a joinable K3s cluster needs a fixed
  // address before any guest boots. Both CIDR and gateway are required here.
  if (!CIDR_RE.test(s.cidr)) {
    errs.push({
      field: "cidr",
      message: "The control-plane IP must be CIDR notation, e.g. 192.168.1.50/24.",
    });
  }
  if (s.gateway === "") {
    errs.push({ field: "gateway", message: "A gateway is required for the control-plane IP." });
  } else if (!IP_RE.test(s.gateway)) {
    errs.push({ field: "gateway", message: "Gateway must be an IPv4 address, e.g. 192.168.1.1." });
  }

  // At least one SSH key — cluster nodes lock password login, so a key is the
  // only way in (the backend rejects an empty sshKeys with a 400).
  if (parseSshKeys(s.sshKeys).length === 0) {
    errs.push({
      field: "sshKeys",
      message: "At least one SSH public key is required — cluster nodes lock password login.",
    });
  }

  // VMIDs — the start id must be a valid VMID, and the whole allocated block must
  // be distinct, in range, and (best-effort) free.
  const start = Number(s.startVmid);
  if (s.startVmid === "" || !Number.isInteger(start) || start < VMID_MIN || start > VMID_MAX) {
    errs.push({
      field: "startVmid",
      message: `Starting VMID must be an integer between ${VMID_MIN} and ${VMID_MAX}.`,
    });
  } else {
    // The typed start becomes the server VMID: flag it directly when it's already
    // in use so the operator picks a free base (the allocator would otherwise
    // silently shift the whole block, which is surprising).
    if (taken.has(start)) {
      errs.push({
        field: "startVmid",
        message: `VMID ${start} is already in use — pick another start.`,
      });
    }
    const { serverVmid, agentVmids } = allocateSetVmids(start, count, taken);
    const all = [serverVmid, ...agentVmids];
    if (all.some((v) => v > VMID_MAX)) {
      errs.push({ field: "startVmid", message: `The VMID block runs past ${VMID_MAX}.` });
    }
    if (new Set(all).size !== all.length) {
      errs.push({ field: "startVmid", message: "The allocated VMIDs are not all distinct." });
    }
  }

  for (const t of s.tags) {
    if (!TAG_RE.test(t)) {
      errs.push({
        field: "tags",
        message: `Invalid tag "${t}" — lowercase letters, digits, . - _ only.`,
      });
    }
  }

  return errs;
}

/**
 * Shape the CreateSetRequest wire payload. Call only when validateSetForm passes.
 * agentCount is clamped, agentVmids length === the clamped count (the invariant
 * the server enforces), and serverIp is the STATIC control-plane address; agents
 * get DHCP server-side (never sent here).
 */
export function toCreateSetRequest(
  s: SetFormState,
  bounds: AgentBounds,
  taken: ReadonlySet<number> = new Set(),
): CreateSetRequest {
  const agentCount = clampAgentCount(s.agentCount, bounds);
  const { serverVmid, agentVmids } = allocateSetVmids(Number(s.startVmid), agentCount, taken);
  const sshKeys = parseSshKeys(s.sshKeys);

  return {
    serviceId: s.serviceId,
    projectId: s.projectId,
    name: s.name,
    node: s.node,
    storage: s.storage,
    bridge: s.bridge,
    ...(s.vlanTag !== "" ? { vlanTag: Number(s.vlanTag) } : {}),
    firewall: s.firewall,
    ...(sshKeys.length > 0 ? { sshKeys } : {}),
    ...(s.tags.length > 0 ? { tags: s.tags } : {}),
    agentCount,
    serverVmid,
    agentVmids,
    serverIp: { mode: "static", cidr: s.cidr, gateway: s.gateway },
  };
}
