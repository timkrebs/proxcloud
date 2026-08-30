// Status vocabulary — the exact strings and color mapping from the design.
// Every status dot, pill, and command-bar enablement derives from here.

export const STATUS_GREEN = "#107C10";
export const STATUS_GRAY = "#605E5C";
export const STATUS_RED = "#D13438";
export const STATUS_BLUE = "#0078D4";

const GREEN = new Set([
  "running",
  "healthy",
  "available",
  "active",
  "succeeded",
  "created",
  "attached",
  "online",
  "ok",
  "ready", // a deployment set whose members all came up (ADR-0029)
]);
const GRAY = new Set(["stopped", "pending", "offline", "tombstoned"]);
const RED = new Set(["failed", "deny", "error"]);

/** Design contract: green for terminal-good, gray for stopped/pending,
 *  red for failures, blue for everything transitional. */
export function statusColor(status: string): string {
  const s = status.toLowerCase();
  if (GREEN.has(s)) return STATUS_GREEN;
  if (GRAY.has(s)) return STATUS_GRAY;
  if (RED.has(s)) return STATUS_RED;
  return STATUS_BLUE;
}

/** Capitalized display form of a canonical status ("running" → "Running"). */
export function statusLabel(status: string): string {
  if (!status) return "";
  return status.charAt(0).toUpperCase() + status.slice(1);
}

export interface CommandBarState {
  connect: boolean;
  start: boolean;
  stop: boolean;
  restart: boolean;
  delete: boolean;
}

/** Command-bar enablement per the design: Connect/Stop/Restart need Running,
 *  Start needs Stopped, Delete is blocked while any transition is in flight
 *  (and the backend additionally requires the guest to be stopped). */
export function commandBarState(status: string): CommandBarState {
  const s = status.toLowerCase();
  const running = s === "running";
  const stopped = s === "stopped";
  const busy = !running && !stopped;
  return {
    connect: running,
    start: stopped,
    stop: running,
    restart: running,
    delete: !busy,
  };
}
