// Pure presentation helpers for the deployment-set list & detail UI. Kept
// separate from the create-form logic (deploymentSetForm.ts) and unit-tested.
//
// Contract note: the DeploymentSet view type carries NO set-level `name`, and the
// persisted GET/list member views carry no member `name` (only the live SSE
// frame and the create response do). So these helpers derive a stable, honest
// display label from whatever is present, falling back to role + VMID / the set
// id — never a fabricated value.

import type { DeploymentSet, DeploymentSetMember } from "@/lib/api/generated/types";

/**
 * A stable human name for a set: the server member's base name (its `<base>-server`
 * with the suffix stripped) when the live frame/create response supplied it, else
 * an agent's base name, else a short id token (`set-<8hex>`). The SAME token the
 * typed-name delete dialog shows and asks the operator to type — so the guard is
 * always honest about what to enter.
 */
export function setBaseName(set: DeploymentSet): string {
  const server = set.members.find((m) => m.role === "server" && m.name);
  if (server?.name) return server.name.replace(/-server$/, "");
  const agent = set.members.find((m) => m.name);
  if (agent?.name) return agent.name.replace(/-agent-\d+$/, "");
  return `set-${set.id.slice(0, 8)}`;
}

/** A member's display label — its name when known, else `<role> · vmid <n>`. */
export function memberLabel(m: DeploymentSetMember): string {
  if (m.name) return m.name;
  const role = m.role || "member";
  return `${role} · vmid ${m.vmid}`;
}

/** Ordering rank so the server (control plane) sorts before its agents. */
function roleRank(role: string): number {
  if (role === "server") return 0;
  if (role === "agent") return 1;
  return 2;
}

/** Members ordered server-first, then agents, then anything else, by VMID. */
export function orderedMembers(set: DeploymentSet): DeploymentSetMember[] {
  return [...set.members].sort((a, b) => {
    const ra = roleRank(a.role);
    const rb = roleRank(b.role);
    return ra === rb ? a.vmid - b.vmid : ra - rb;
  });
}

/**
 * True while a set is in a transitional status the detail view should poll on
 * (mirrors the deployments/[id] refetch gate).
 */
export function isSetTransitional(status: string): boolean {
  return status === "provisioning" || status === "deleting";
}

/**
 * True when the set still has live (non-tombstoned) members — i.e. guests that
 * exist and are quota-charged. The delete guard uses this to warn the operator to
 * Stop the cluster first (DeleteSet purges directly and expects stopped guests).
 */
export function hasLiveMembers(set: DeploymentSet): boolean {
  return set.members.some((m) => m.status !== "tombstoned");
}

/** The control-plane member, if present (carries the reachable connection). */
export function serverMember(set: DeploymentSet): DeploymentSetMember | undefined {
  return set.members.find((m) => m.role === "server");
}
