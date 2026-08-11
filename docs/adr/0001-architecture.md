# ADR-0001: Core architecture

Date: 2026-08-11 · Status: accepted

## Context

Proxcloud is a self-service control plane for a single Proxmox VE homelab
server, modeled on the Azure Portal, with a finished Claude Design project as
the visual source of truth. Requirements: real data only, secrets server-side,
async task honesty, one-command dev.

## Decision

- **Monorepo**: `frontend/` (Next.js 15 App Router, TS, Tailwind v4, TanStack
  Query) + `backend/` (Go 1.23+, chi). The Go backend is the only component
  that talks to Proxmox (`/api2/json`), via `github.com/luthermonson/go-proxmox`
  behind our own `ProxmoxClient` interface, with raw HTTP where the library is
  thin (storage content, snapshot rollback/delete, firewall rule edits,
  websocket dial).
- **Auth**: single admin user from env; stateless HMAC-signed session cookie.
  The Proxmox API token never reaches the browser.
- **Wire contract**: Go structs in `backend/api/types` are the single source of
  truth; TypeScript types generated with tygo into
  `frontend/src/lib/api/generated/types.ts` (committed, CI diff-checked).
  Timestamps RFC3339, sizes raw bytes, percents 0–100; the frontend owns
  formatting.
- **Live data**: SSE (`/api/events`) with named events `metrics` (5s refcounted
  node poller), `task`, `deployment`; task status additionally polled by the
  UI at 2s while running.
- **Task model (hybrid)**: the global activity log proxies `/cluster/tasks`
  verbatim; an in-memory registry tracks only Proxcloud-initiated tasks,
  deployments, and the notification ring. A backend restart loses only
  friendly labels/notifications, never task truth.
- **Design scope**: the design's multi-tenant fantasy catalog is adapted to
  Proxmox reality — VM + LXC only, "projects" = Proxmox resource pools,
  tenant = the real cluster. Visual system and interaction model are kept
  faithfully.
- **Pricing**: optional flat-rate config (env); all cost UI hidden when unset.
- **Dev**: docker-compose (backend air hot-reload + next dev); Next rewrites
  proxy `/api` to the backend (no CORS). The console WebSocket connects to the
  backend origin directly (rewrites can't proxy WS) using one-shot session ids.

## Consequences

- One typed API layer, testable backend (mocked `ProxmoxClient`), no CORS
  complexity, and honest failure states everywhere.
- Console requires an extra credential pair because Proxmox websockets reject
  API-token auth (ADR-0004 will detail the ticket flow).
