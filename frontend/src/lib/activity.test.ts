// Activity vocabulary → friendly-label mapping. Known audit codes translate;
// guest.<verb> lifecycle codes render past-tense; PVE task labels and unknown
// codes pass through untouched (never a fabricated label).
import { describe, expect, it } from "vitest";

import { actionLabel, sourceLabel, targetLabel } from "@/lib/activity";

describe("actionLabel", () => {
  const cases: [string, string][] = [
    ["project.create", "Created project"],
    ["project.quota.update", "Updated project quota"],
    ["tenant.quota.update", "Updated directory quota"],
    ["guest.create", "Created guest"],
    ["guest.snapshot.rollback", "Rolled back snapshot"],
    ["guest.firewall.update", "Updated firewall"],
    ["reservation.reclaimed", "Reclaimed reservation"],
    ["guest.start", "Started guest"],
    ["guest.reboot", "Rebooted guest"],
  ];
  it.each(cases)("maps %s → %s", (action, label) => {
    expect(actionLabel(action)).toBe(label);
  });

  it("renders an unknown guest verb generically", () => {
    expect(actionLabel("guest.migrate")).toBe("Migrated guest");
    expect(actionLabel("guest.snapshot.frobnicate")).toBe("Guest snapshot frobnicate");
  });

  it("passes a PVE task label through unchanged", () => {
    expect(actionLabel("Start virtual machine")).toBe("Start virtual machine");
  });

  it("renders an em dash for an empty action", () => {
    expect(actionLabel("")).toBe("—");
  });
});

describe("targetLabel", () => {
  it("labels a guest by VMID", () => {
    expect(targetLabel("guest", "106")).toBe("VMID 106");
  });
  it("prefers the project name for a project target", () => {
    expect(targetLabel("project", "p-1", "Web")).toBe("Web");
    expect(targetLabel("project", "p-1")).toBe("p-1");
  });
  it("labels a tenant target as Directory", () => {
    expect(targetLabel("tenant", "")).toBe("Directory");
  });
});

describe("sourceLabel", () => {
  it("maps audit → Proxcloud and task → Proxmox", () => {
    expect(sourceLabel("audit")).toBe("Proxcloud");
    expect(sourceLabel("task")).toBe("Proxmox");
  });
});
