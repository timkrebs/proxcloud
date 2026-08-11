# Proxcloud

Self-service cloud control plane for a Proxmox VE homelab, modeled on the Azure Portal.
Goal: deploy and manage real infrastructure (VMs + LXC containers) on the Proxmox server
directly from the Proxcloud UI, with live resource stats.

## Architecture

```
proxcloud/
├── frontend/     # Next.js 15 (App Router), TypeScript, Tailwind, TanStack Query
├── backend/      # Go 1.23+, chi router, REST + SSE, sole component talking to Proxmox
├── docker-compose.yml
└── docs/adr/     # Architecture Decision Records
```

- The **frontend never talks to Proxmox directly**. All Proxmox calls go through the Go backend.
- Backend uses `github.com/luthermonson/go-proxmox` (raw `/api2/json` HTTP only where the library falls short).
- Live metrics flow backend → frontend via SSE (`/api/events/metrics`), backed by polling
  `nodes/{node}/status` and `rrddata` endpoints.
- Long-running Proxmox operations are async: submit → get UPID → UI polls
  `/api/tasks/{upid}` which proxies `nodes/{node}/tasks/{upid}/status`.

## Iron rules (all agents)

1. **No mock data. Ever.** Every number in the UI comes from the Proxmox API. Missing data → explicit error/empty state, never a fabricated value.
2. **Secrets stay server-side.** `PROXMOX_TOKEN_SECRET` must never reach the browser, logs, or git. Config via env vars only (see `.env.example`).
3. **Design fidelity.** The imported Claude Design project (`Proxcloud.dc.html`) is the source of truth for tokens, layout, and screens. Design tokens live in the Tailwind config / CSS variables — never hardcode colors or spacing.
4. **Azure interaction model**: global left nav, searchable "All resources" list, blade-style detail pages, multi-step "Create a resource" wizard, activity log.
5. **Honest task states.** Creation/deletion shows real Proxmox task progress and surfaces the actual Proxmox error message on failure.
6. Every significant technical decision gets an ADR in `docs/adr/` before implementation.

## Environment

```
PROXMOX_URL=https://<host>:8006
PROXMOX_TOKEN_ID=user@pam!tokenname
PROXMOX_TOKEN_SECRET=<secret>
PROXMOX_TLS_INSECURE=true|false      # homelab self-signed certs
SESSION_SECRET=<random>
ADMIN_USER=admin
ADMIN_PASSWORD_HASH=<bcrypt>
```

## Commands

```bash
# Backend
cd backend && go build ./... && go test ./...
go vet ./... && staticcheck ./...

# Frontend
cd frontend && npm run dev
npm run build && npm run lint && npm run test

# Full stack
docker-compose up --build
```

## Conventions

- **Go**: idiomatic, table-driven tests, interfaces around the Proxmox client (`ProxmoxClient` interface + mock for tests), context timeouts on every outbound call, structured logging with `slog`. Handle Proxmox 401/403/595 explicitly.
- **TypeScript**: strict mode, no `any`. API types generated/shared from Go structs (single source of truth in `backend/api/types`).
- **Commits**: conventional commits (`feat:`, `fix:`, `refactor:`, `test:`, `docs:`), one logical change per commit.
- **Branches**: work on feature branches; `main` stays green (build + tests pass).

## Definition of done (any feature)

- [ ] Works against a real Proxmox server (or the mocked client in tests — but the wiring is real)
- [ ] Loading skeleton, empty state, and error state implemented
- [ ] Backend handler has table-driven tests with mocked Proxmox client
- [ ] No secrets in code or logs
- [ ] Matches the design tokens/layout
- [ ] README/docs updated if setup or API surface changed
