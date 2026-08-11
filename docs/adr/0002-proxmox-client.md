# ADR-0002: Proxmox client — wrap go-proxmox, own the decoding

Date: 2026-08-11 · Status: accepted

## Context

`github.com/luthermonson/go-proxmox` v0.8.1 covers most of `/api2/json`,
but its typed wrappers have quirks (cached version, stringly errors,
lossy numeric decoding) and no structured status-code error type.

## Decision

- Keep the library for transport (auth header, data-key unwrap, raw
  Get/Post/Put/Delete) but decode every response into our own wire
  structs behind the `proxmox.Client` interface.
- Lenient `flexInt64` decoding for PVE's byte counters (scientific
  notation above ~1PB) and strings-or-numbers loadavg.
- RRD null samples are dropped, never fabricated; `cpu` fractions are
  converted to 0–100 at the client boundary so the whole app speaks
  percent.
- One error mapper (`mapErr`) classifies failures into the stable
  envelope codes; the verbatim PVE message always travels in
  `pveMessage`. PVE puts a finished task's exit status in `status` on
  `/cluster/tasks` but in `exitstatus` on the task-status endpoint — the
  client normalizes both (caught against the live server).

## Consequences

Handlers depend on one interface (mockable, table-testable); library
upgrades touch a single package.
