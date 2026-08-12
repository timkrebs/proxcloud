// Activity vocabulary → friendly labels (Phase 4). The audit feed emits stable
// action codes (guest.create, project.quota.update, guest.<verb> lifecycle, …);
// the PVE task feed emits already-human labels. This maps the codes to readable
// text and leaves anything unknown (task labels, new verbs) untouched — honest,
// never a fabricated label. Pure + unit-tested.

const ACTION_LABELS: Record<string, string> = {
  "project.create": "Created project",
  "project.rename": "Renamed project",
  "project.update": "Updated project",
  "project.delete": "Deleted project",
  "project.quota.update": "Updated project quota",
  "tenant.quota.update": "Updated directory quota",
  "tenant.create": "Created directory",
  "guest.create": "Created guest",
  "guest.delete": "Deleted guest",
  "guest.config.update": "Updated configuration",
  "guest.disk.resize": "Resized disk",
  "guest.snapshot.create": "Created snapshot",
  "guest.snapshot.rollback": "Rolled back snapshot",
  "guest.snapshot.delete": "Deleted snapshot",
  "guest.firewall.update": "Updated firewall",
  "guest.console.open": "Opened console",
  "reservation.reclaimed": "Reclaimed reservation",
};

// guest.<verb> lifecycle verbs (start|stop|reboot|…) → past-tense phrasing.
const LIFECYCLE_VERBS: Record<string, string> = {
  start: "Started guest",
  stop: "Stopped guest",
  shutdown: "Shut down guest",
  reboot: "Rebooted guest",
  reset: "Reset guest",
  suspend: "Suspended guest",
  resume: "Resumed guest",
  migrate: "Migrated guest",
};

/** Friendly label for an activity action code (PVE labels pass through). */
export function actionLabel(action: string): string {
  if (!action) return "—";
  const known = ACTION_LABELS[action];
  if (known) return known;
  if (action.startsWith("guest.")) {
    const verb = action.slice("guest.".length);
    if (LIFECYCLE_VERBS[verb]) return LIFECYCLE_VERBS[verb];
    return `Guest ${verb.replace(/[._]/g, " ")}`;
  }
  return action;
}

/** Human target description from targetType/targetId (+ optional project name). */
export function targetLabel(targetType: string, targetId: string, projectName?: string): string {
  if (!targetId && !targetType) return "—";
  switch (targetType) {
    case "guest":
      return targetId ? `VMID ${targetId}` : "Guest";
    case "project":
      return projectName || targetId || "Project";
    case "tenant":
      return "Directory";
    case "quota":
      return projectName ? `Quota — ${projectName}` : "Quota";
    case "member":
      return targetId || "Member";
    default:
      return targetId || "—";
  }
}

/** Source badge text: the audit feed is Proxcloud's own, the task feed is Proxmox's. */
export function sourceLabel(source: string): "Proxcloud" | "Proxmox" {
  return source === "audit" ? "Proxcloud" : "Proxmox";
}
