"use client";
// Recently-viewed resources, kept client-side in localStorage — the design's
// "Recent resources" card without inventing server state. Entries are
// validated against the live resource list before display.

const KEY = "proxcloud.recent";
const CAP = 8;

export interface RecentEntry {
  id: string; // "qemu/101"
  type: "qemu" | "lxc";
  vmid: number;
  name: string;
  node: string;
  viewedAt: string; // ISO
}

function read(): RecentEntry[] {
  if (typeof window === "undefined") return [];
  try {
    const raw = window.localStorage.getItem(KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as RecentEntry[];
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

export function recordRecent(e: Omit<RecentEntry, "viewedAt">) {
  if (typeof window === "undefined") return;
  const list = read().filter((r) => r.id !== e.id);
  list.unshift({ ...e, viewedAt: new Date().toISOString() });
  try {
    window.localStorage.setItem(KEY, JSON.stringify(list.slice(0, CAP)));
  } catch {
    // storage full/blocked — recents are a nicety, never an error
  }
}

export function listRecent(): RecentEntry[] {
  return read();
}
