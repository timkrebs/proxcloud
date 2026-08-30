// Status contract tests — the design's exact color vocabulary and the
// command-bar enablement matrix.
import { describe, expect, it } from "vitest";

import {
  commandBarState,
  statusColor,
  STATUS_BLUE,
  STATUS_GRAY,
  STATUS_GREEN,
  STATUS_RED,
} from "@/lib/status";

describe("statusColor", () => {
  const cases: [string, string][] = [
    ["running", STATUS_GREEN],
    ["Running", STATUS_GREEN],
    ["succeeded", STATUS_GREEN],
    ["attached", STATUS_GREEN],
    ["online", STATUS_GREEN],
    ["stopped", STATUS_GRAY],
    ["pending", STATUS_GRAY],
    ["offline", STATUS_GRAY],
    ["failed", STATUS_RED],
    ["error", STATUS_RED],
    ["starting", STATUS_BLUE],
    ["stopping", STATUS_BLUE],
    ["restarting", STATUS_BLUE],
    ["provisioning", STATUS_BLUE],
    ["deleting", STATUS_BLUE],
    ["resizing", STATUS_BLUE],
    ["in progress", STATUS_BLUE],
  ];
  it.each(cases)("%s → %s", (status, color) => {
    expect(statusColor(status)).toBe(color);
  });
});

describe("commandBarState", () => {
  it("running: connect/stop/restart enabled, start disabled, delete allowed", () => {
    expect(commandBarState("running")).toEqual({
      connect: true,
      start: false,
      stop: true,
      restart: true,
      delete: true,
    });
  });

  it("stopped: only start (and delete) enabled", () => {
    expect(commandBarState("stopped")).toEqual({
      connect: false,
      start: true,
      stop: false,
      restart: false,
      delete: true,
    });
  });

  const busy = ["starting", "stopping", "restarting", "provisioning", "deleting", "resizing"];
  it.each(busy)("%s: everything disabled including delete", (status) => {
    expect(commandBarState(status)).toEqual({
      connect: false,
      start: false,
      stop: false,
      restart: false,
      delete: false,
    });
  });
});
