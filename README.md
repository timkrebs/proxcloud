# Proxcloud

A self-service cloud control plane for your Proxmox VE homelab, modeled on the
Azure Portal. Deploy and manage real VMs and LXC containers with live resource
stats — every number on screen comes from the Proxmox API.

```
proxcloud/
├── frontend/     # Next.js 15 (App Router), TypeScript, Tailwind, TanStack Query
├── backend/      # Go, chi router — the only component that talks to Proxmox
├── docs/adr/     # Architecture Decision Records
└── docker-compose.yml
```

## Quick start

1. Create a Proxmox API token (see below) and copy the env template:

   ```bash
   cp .env.example .env   # then fill in the values
   ```

2. Start everything:

   ```bash
   docker-compose up --build
   ```

   Frontend: http://localhost:3000 · Backend API: http://localhost:8080

Native dev (Go + Node installed): `make dev`.

## Proxmox API token setup

```bash
# Role with every privilege Proxcloud uses
pveum role add Proxcloud -privs "VM.Audit VM.Allocate VM.Clone VM.Config.CDROM \
  VM.Config.CPU VM.Config.Cloudinit VM.Config.Disk VM.Config.HWType \
  VM.Config.Memory VM.Config.Network VM.Config.Options VM.PowerMgmt VM.Console \
  VM.Monitor VM.Snapshot VM.Snapshot.Rollback Datastore.Audit \
  Datastore.AllocateSpace Datastore.AllocateTemplate Sys.Audit Pool.Audit SDN.Use"

pveum user add proxcloud@pve --password '<strong password>'
pveum aclmod / -user proxcloud@pve -role Proxcloud
pveum user token add proxcloud@pve portal --privsep 0   # token inherits user perms
```

Put the printed token id/secret into `.env` as `PROXMOX_TOKEN_ID` /
`PROXMOX_TOKEN_SECRET`.

**Console note:** Proxmox rejects API-token auth on its console websockets, so
the embedded noVNC/xterm console additionally needs
`PROXMOX_CONSOLE_USER`/`PROXMOX_CONSOLE_PASSWORD` (a real PVE login, e.g. the
`proxcloud@pve` user above). Without them the app runs fine with the console
feature disabled.

## Decisions log

- Design source of truth: the Claude Design project (`Proxcloud.dc.html`);
  tokens live in the Tailwind theme, screens adapted to real Proxmox content
  (VM + LXC; "projects" = Proxmox resource pools). See `docs/adr/`.
- Pricing UI is optional and driven by flat rates in `.env`; hidden when unset.

*(Docs grow with each milestone — see `docs/adr/` for decisions.)*
