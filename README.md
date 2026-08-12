# Proxcloud

A self-service cloud control plane for your Proxmox VE homelab, modeled on the
Azure Portal. Deploy and manage real VMs and LXC containers with live resource
stats — **every number on screen comes from the Proxmox API**.

## What it does

- **Dashboard** — node health, CPU/RAM/storage usage, guest counts, live
  charts fed by SSE, recently viewed resources, optional cost estimate
- **All resources** — every VM and container: VMID, node, status, CPU, RAM,
  uptime, tags; search, sort, filters, bulk start/stop
- **Resource blades** (Azure-style) — Overview with live sparklines and guest
  IPs, Activity log, Access control, Tags, Networking (+ firewall), Disks
  (grow-only resize), Snapshots (create/rollback/delete), Size (T-shirt
  presets + exact values), Metrics, **Console** (noVNC for VMs, xterm for
  containers)
- **Create wizard** — seven tabs (Basics → Review + create); VM from ISO,
  VM clone (full/linked), or LXC from a template. Every dropdown is real:
  nodes, next free VMID, storages, ISOs, templates, bridges, pools
- **Honest async** — every mutation is a real Proxmox task (UPID): live
  deployment progress, transitional statuses, a notification bell, and the
  verbatim Proxmox error when something fails
- **Activity log** — the cluster task history with a live log-tail flyout

```
proxcloud/
├── frontend/     # Next.js 15 (App Router), TypeScript, Tailwind v4, TanStack Query
├── backend/      # Go, chi — the only component that talks to Proxmox
├── docs/adr/     # Architecture Decision Records
└── docker-compose.yml
```

## Quick start

1. Create a Proxmox API token (next section) and configure the env:

   ```bash
   cp .env.example .env    # fill in PROXMOX_*, DATABASE_URL, SECRETS_KEY (ADMIN_* optional)
   ```

2. Start everything:

   ```bash
   docker-compose up --build
   ```

   Portal: <http://localhost:3000> · API: <http://localhost:8080>
   (host ports configurable: `BACKEND_PORT=8090 docker-compose up`)

Native dev (Go ≥1.23 + Node ≥20): `make dev` — backend with air hot-reload
on `LISTEN_ADDR`, `next dev` on :3000.

## Proxmox API token setup

```bash
# Role with every privilege Proxcloud uses
pveum role add Proxcloud -privs "VM.Audit VM.Allocate VM.Clone VM.Config.CDROM \
  VM.Config.CPU VM.Config.Cloudinit VM.Config.Disk VM.Config.HWType \
  VM.Config.Memory VM.Config.Network VM.Config.Options VM.PowerMgmt VM.Console \
  VM.Monitor VM.Snapshot VM.Snapshot.Rollback Datastore.Audit \
  Datastore.AllocateSpace Datastore.AllocateTemplate Sys.Audit Pool.Audit"

pveum user add proxcloud@pve --password '<strong password>'
pveum aclmod / -user proxcloud@pve -role Proxcloud
pveum user token add proxcloud@pve portal --privsep 0   # token inherits user perms
```

Put the printed values into `.env` as `PROXMOX_TOKEN_ID`
(`proxcloud@pve!portal`) and `PROXMOX_TOKEN_SECRET`.

### Console credentials (optional)

Proxmox **rejects API-token auth on its console websockets**
(vncwebsocket/termproxy), so the embedded console needs a real login:

```
PROXMOX_CONSOLE_USER=proxcloud@pve
PROXMOX_CONSOLE_PASSWORD=<the user's password>
```

Without them the app runs fine and the Console blade explains why it is
disabled. The ticket never reaches the browser — the backend bridges the
websocket (see `docs/adr/0003-console-ticket-auth.md`).

### Pricing (optional)

Flat monthly rates turn on the cost UI (wizard estimate + dashboard card):

```
PRICING_CURRENCY=EUR
PRICING_VCPU_MONTH=3.50
PRICING_RAM_GB_MONTH=1.20
PRICING_DISK_GB_MONTH=0.05
```

Unset ⇒ all cost elements are hidden. Estimates are labeled as such.

## Environment reference

| Variable | Required | Purpose |
|---|---|---|
| `PROXMOX_URL` | ✔ | Base URL, e.g. `https://pve01:8006` (no `/api2/json`) |
| `PROXMOX_TOKEN_ID` / `PROXMOX_TOKEN_SECRET` | ✔ | API token (`user@realm!name`) |
| `PROXMOX_TLS_INSECURE` | — | `true` for self-signed homelab certs |
| `DATABASE_URL` | ✔ | Postgres DSN (e.g. `postgres://proxcloud:proxcloud@postgres:5432/proxcloud?sslmode=disable`; TLS required in production) |
| `SECRETS_KEY` | ✔ | 32 bytes, encrypts secrets at rest (`openssl rand -hex 32`) |
| `ADMIN_USER` | —† | First-run seed only: platform admin on an empty database |
| `ADMIN_PASSWORD_HASH` | —† | bcrypt hash (preferred) — `htpasswd -bnBC 10 "" 'pw' \| tr -d ':\n'` |
| `ADMIN_PASSWORD` | —† | Plaintext alternative, hashed at boot |
| `PROXMOX_CONSOLE_USER` / `_PASSWORD` | — | Enables the embedded console |
| `PRICING_*` | — | Enables cost UI (see above) |
| `SESSION_IDLE_TTL` / `SESSION_ABSOLUTE_TTL` | — | Session timeouts (default `12h` / `720h`) |
| `RECONCILER_INTERVAL` / `RESERVATION_TTL` | — | Reconciler cadence + stale pending-reservation reclaim (default `5m` / `45m`) |
| `LISTEN_ADDR` | — | Backend listen address (default `:8080`) |

† Optional. If `ADMIN_USER` is set, one of `ADMIN_PASSWORD_HASH` / `ADMIN_PASSWORD`
is required. With no `ADMIN_*`, create the first admin via the first-run screen.

### First run & the env-admin cutover

On an **empty** database Proxcloud has no users. Create the first platform
admin one of two ways:

- **First-run screen** — visit the portal; `GET /api/auth/bootstrap-status`
  reports `needsBootstrap: true`, and the sign-in page shows a one-time
  "Create platform administrator" form (`POST /api/auth/bootstrap`).
- **Env-admin cutover** — set `ADMIN_USER` + `ADMIN_PASSWORD` (or
  `ADMIN_PASSWORD_HASH`) before first boot. The backend seeds a single admin
  and logs a loud WARN. Sign in with the **email** `<ADMIN_USER>@proxcloud.local`
  (or `ADMIN_USER` verbatim if it already contains `@`), then change the password
  in **Settings** and remove `ADMIN_*` from the environment. The seed runs only
  while the users table is empty; afterwards `ADMIN_*` is ignored.

## Development

```bash
make check    # go vet + build + tests, next lint + vitest, tygo diff-check
make test     # backend + frontend test suites
make gen-types  # regenerate frontend/src/lib/api/generated/types.ts from Go
```

- The wire contract lives in `backend/api/types` (Go) and is exported to
  TypeScript with tygo; the generated file is committed and diff-checked.
- Timestamps are RFC3339, sizes raw bytes, percents 0–100; the frontend owns
  all display formatting.
- Design source of truth: the Claude Design project, inventoried in
  `docs/design/` (visual + behavioral analysis); tokens live in
  `frontend/src/app/globals.css` (`@theme`).

## Decisions and limitations

- Screens are the design's, content is real Proxmox: "projects" map to
  Proxmox resource pools; the catalog offers what the server can actually
  create (VM + LXC). No fake products, no mock data — missing data renders
  an explicit empty/error state.
- Cloud-init v1 supports user/password/SSH keys/DNS/`ipconfig0` — no custom
  user-data snippets (the storage upload API doesn't take them).
- Linked clones require a template source and stay on its storage (PVE rule,
  enforced in wizard + server).
- Deleting a guest requires it to be stopped (server enforces 409) and the
  typed-name confirmation in the UI.
- Deployment progress is in-memory: after a backend restart the progress
  page 404s with a pointer to the activity log — task truth stays in Proxmox.
- ADRs: `docs/adr/`.

## Security notes

- Session cookies are `Secure` + `HttpOnly` + `SameSite=Lax` by default
  (browsers accept Secure cookies on `http://localhost`); a 24 h sliding
  window re-issues them on activity. `PROXCLOUD_INSECURE_COOKIES=true` is a
  dev-only opt-out and refuses to start when `PROXCLOUD_ENV=production`.
- Login is rate-limited (5/min per IP) with bounded concurrent bcrypt work.
- Deleting a guest requires the typed name in the request body — the server
  re-verifies it against the live guest, so the confirmation is not just UI.
- State-changing requests reject mismatched `Origin`/`Referer` headers; set
  `FRONTEND_ORIGIN` for any non-localhost deployment (it also becomes the
  only origin the console websocket accepts).
- The console websocket additionally requires a signed-in portal session
  wherever the backend is cookie-reachable, and one-shot ids are redacted
  from access logs.
- Deploy for real use behind TLS (reverse proxy or tunnel). `npm audit`
  currently reports transitive advisories in Next 15.5's pinned postcss/
  sharp; neither is reachable from user input here — tracked for the next
  Next.js bump.
