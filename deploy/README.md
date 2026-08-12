# `deploy/` — Proxcloud delivery infrastructure (WS4)

This directory is the **one piece of infra managed outside Proxcloud**: Proxcloud
cannot deploy the guests it runs on, so Terraform provisions them and on-guest
scripts run the blue/green cutover. Authoritative design: **ADR-0014** (CD
workflow topology), **ADR-0015** (blue/green Caddy proxy), and
`.claude/agents/release-engineer.md`. `RELEASING.md` is the operator's one-pager.

```
deploy/
├── terraform/            # bpg/proxmox — provisions proxcloud-staging + proxcloud-prod
│   ├── versions.tf variables.tf main.tf staging.tf prod.tf outputs.tf
│   ├── terraform.tfvars.example
│   └── templates/cloud-init.yaml.tftpl
└── host/                 # copied to /opt/proxcloud/ on each guest at provision time
    ├── common/bin/       # deploy-wrapper.sh · first-boot.sh · gen-postgres-cert.sh
    ├── prod/             # ADR-0015 layout: data/ blue/ green/ caddy/ state/ bin/deploy.sh
    └── staging/          # single stack: docker-compose.yml + caddy/ + bin/deploy.sh
```

At provision time each guest's `/opt/proxcloud/bin` is assembled from
`host/common/bin/*` **plus** `host/<env>/bin/*`, and `host/<env>/*` overlays the
rest of `/opt/proxcloud/`.

---

## 1. Provisioning the guests (Terraform)

**Defaults** (Tim-approved, all overridable in `terraform.tfvars`): node `pve01`,
storage `local-lvm`, bridge `vmbr0`, no VLAN, **DHCP**, domain `proxcloud.lab`.
- `proxcloud-staging` — **LXC**, 2 cores / 4 GB / 32 GB.
- `proxcloud-prod` — **VM**, 4 cores / 8 GB / 64 GB.

### Supply the token via env (never in a file)

The Proxmox API token is **not** a Terraform variable, so it can never land in a
`.tfvars` next to state. Provide it at apply time:

```bash
cd deploy/terraform
cp terraform.tfvars.example terraform.tfvars     # edit: keys, domain, IPs
export PROXMOX_VE_API_TOKEN='proxcloud@pve!tf=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx'

terraform init
terraform fmt -check
terraform validate
terraform plan        # review — this touches the REAL pve01
terraform apply       # Tim's authorized step; creates real guests
```

Notes:
- **bpg uploads the cloud-init snippet over SSH** to the node, so the provider's
  `ssh { username = "root" }` needs SSH access to `pve01` (agent or key). The
  token alone covers the API calls; the snippet upload needs the SSH hop.
- **DHCP + provisioners:** the file/remote-exec provisioners SSH to the guests.
  With DHCP the lease is unknown until boot; `*_provision_host` defaults to the
  DNS names (`proxcloud-{staging,prod}.<domain>`). If your lab does not register
  leases in DNS, `apply` compute first with `-var run_provisioners=false`, read
  the prod IP from the `prod_vm_ipv4_addresses` output, set
  `prod_provision_host` / `staging_provision_host`, then `apply` again. Or switch
  to `ip_mode = "static"`.
- **Docker-in-LXC:** staging is an unprivileged LXC with `nesting + keyctl`,
  which runs Docker on modern PVE kernels. If Docker fails to start there, set
  the container to `unprivileged = false` (edit `staging.tf`) and re-apply.

### First-boot flow (what apply does)

1. VM: cloud-init creates the sudo admin user + guest-agent. LXC: root keys set.
2. Provisioner runs `first-boot.sh` → Docker CE + compose plugin, the locked
   key-only `deploy` user (docker group), `jq`, `openssl`, unattended-upgrades.
3. Provisioner copies `common/` + `<env>/` to `/opt/proxcloud/` and the CI deploy
   **public** key to `/opt/proxcloud/ci-deploy-key.pub`.
4. `bootstrap.sh` creates the external docker networks + dirs + Postgres TLS cert
   + the `active.caddy` symlink + the **forced-command authorized_keys** for the
   deploy user, and hardens ownership.

---

## 2. Placing the real `/opt/proxcloud/.env` (manual, per guest)

App secrets **never** enter git or CI (`release-engineer.md`, ADR-0014 §7). On
each guest, from the template shipped in the tree:

```bash
# prod guest
cd /opt/proxcloud
cp env.example .env            # then edit: PROXMOX_*, SECRETS_KEY, POSTGRES_PASSWORD,
                               # DATABASE_URL (same password!), FRONTEND_ORIGIN, SMTP_*, NTFY_URL
sudo /opt/proxcloud/bootstrap.sh     # re-run: fixes .env perms (600, deploy-owned)
/opt/proxcloud/bin/up-infra.sh       # brings up postgres + caddy
```

Staging is the same minus `up-infra.sh` (its `deploy.sh` brings the single stack
up). `SECRETS_KEY` = `openssl rand -hex 32`. Keep the DB password identical in
`POSTGRES_PASSWORD` and inside `DATABASE_URL`.

**Why `PROXCLOUD_ENV=production` + Postgres TLS:** `config.go` fails closed if
production runs against a non-TLS `DATABASE_URL`. `gen-postgres-cert.sh` issues a
self-signed server cert (owned `70:70`, mode `600`); the app uses
`sslmode=require` (encrypt, no CA verify). To opt out on an isolated network,
unset `PROXCLOUD_ENV` (Dev DB rule) — but staging then no longer mirrors prod.

---

## 3. Networks & loopback port map

Two **external** docker networks on prod (created by `bootstrap.sh`):

| network              | members                                   |
|----------------------|-------------------------------------------|
| `proxcloud-edge`     | caddy + both colors' backend & frontend   |
| `proxcloud-data-net` | both colors' backend + `proxcloud-data-postgres` |

Caddy resolves color containers by name (`proxcloud-blue-backend:8080`,
`proxcloud-green-frontend:3000`). Each color also publishes **loopback-only**
ports for pre-cutover health checks that bypass Caddy:

| color | backend (`/api/*`)   | frontend            |
|-------|----------------------|---------------------|
| blue  | `127.0.0.1:18080`    | `127.0.0.1:13000`   |
| green | `127.0.0.1:28080`    | `127.0.0.1:23000`   |

Staging (single stack) publishes `127.0.0.1:8080` (backend) / `127.0.0.1:3000`
(frontend) for health + smoke; Caddy fronts the public site.

---

## 4. The blue/green switch (how `deploy.sh` works)

`deploy.sh <ref>` (invoked only via the forced-command wrapper):

1. `pg_dump` snapshot to `data/snapshots/` (retain `SNAPSHOT_RETAIN`).
2. Pull the idle color's images at `<ref>`.
3. **Migrate** (expand/contract) and bring the idle **backend** up; health-check
   it on its **loopback** port (`/api/health` + assert `/api/v1/version`
   `.commit == <ref>`) — bypassing Caddy. Any failure aborts with the old color
   still live.
4. Bring the idle frontend up; readiness-check on loopback.
5. **Atomic switch:** `ln -sfn <idle>.caddy caddy/upstream/active.caddy` +
   `caddy reload` (graceful, zero-drop). Old color stays warm.
6. Write `state/live-color` + `state/last-cutover`.

`deploy.sh --rollback` flips the symlink back to the warm color + reloads (after
confirming that color is still healthy). Rollback is **only** a proxy switch —
never a down-migration (down-migrations are dev-only).

### Migrator note (coordinate item)

The dedicated one-shot `migrator` compose service uses `entrypoint:
["/proxcloud", "migrate"]`, which **requires a backend `migrate` subcommand that
applies the embedded migrations and exits** — that does not exist yet (like
`/api/v1/version` once didn't). Until it lands, `deploy.sh` migrates via the
backend's boot-time `store.RunMigrations` (idempotent) and captures its startup
log as the migrator output; a failed migration exits the backend and fails the
health gate **before** any cutover. Flip `USE_MIGRATOR_SERVICE=1` in `.env` once
the subcommand ships.

---

## 5. Self-hosted GitHub runner (its own LXC — NOT these guests)

Per ADR-0014 §7 the runner is **repo-scoped, `--ephemeral`, in its own LXC**,
holds no app secrets, and only invokes the deploy wrapper over SSH.

- Provision a separate small LXC (`proxcloud-runner`) — you can copy the
  `staging.tf` shape or make it by hand. Do **not** put the runner on the prod
  guest.
- Register it repo-scoped + ephemeral:
  ```bash
  ./config.sh --url https://github.com/timkrebs9/proxcloud \
    --token <REGISTRATION_TOKEN> --ephemeral --labels self-hosted,homelab
  ./run.sh   # or install as a service; --ephemeral means it deregisters after one job
  ```
- The runner needs the **deploy SSH private key** to reach the guests' `deploy`
  user. Provide it as a job-injected secret (`STAGING_SSH_KEY` / `PROD_SSH_KEY`
  from the GitHub environments), never baked into the runner image.
- **Updating:** ephemeral runners are single-use; keep the runner LXC's binary
  current with `./config.sh remove` + re-download, or bake a fresh LXC. Because
  it only ever runs already-merged, already-published code and only SSHes the
  wrapper, its blast radius is one validated deploy/rollback.

### If the repo goes public

CI (`ci.yml`) already runs on hosted `ubuntu-latest` with `contents: read` on
untrusted PR code — nothing changes there. Deploy stays gated behind the
protected `production` environment (reviewer: **timkrebs**) and the
`workflow_run` trust boundary, so a fork PR can trigger CI but never
publish/deploy. Set the GHCR packages to public and drop `GHCR_TOKEN` from the
guests' `.env` (public packages need no pull auth).

---

## 6. Security surface (for the security-reviewer)

- **Forced command:** the `deploy` user's `authorized_keys` pins
  `command="/opt/proxcloud/bin/deploy-wrapper.sh",no-port-forwarding,
  no-agent-forwarding,no-X11-forwarding,no-pty`. The wrapper parses **only**
  `$SSH_ORIGINAL_COMMAND`, disables globbing, allows exactly `deploy <ref>` /
  `rollback`, regex-validates `<ref>` (`^[0-9a-f]{40}$` or `^v\d+\.\d+\.\d+`),
  and `exec`s `deploy.sh` — never a shell, never `eval`. The CI key can do
  nothing else. Every attempt is logged to `state/deploy-wrapper.log`.
- **`bin/` is root-owned** (`0755`) so the wrapper/deploy.sh cannot be rewritten
  from the deploy session; the deploy user owns only what it must write (state,
  snapshots, the caddy symlink) and `.env` (`600`).
- **Deploy user is in the `docker` group** (= root-equivalent via the socket).
  That is acceptable *because* the SSH key is forced-command-locked to the
  wrapper — there is no interactive path to that privilege. Documented here so
  it is a conscious decision, not a surprise.
- **No app secret touches CI or the runner.** `PROXMOX_TOKEN_SECRET`,
  `SECRETS_KEY`, DB creds live only in `/opt/proxcloud/.env` (manual, `600`).
- Any change to `deploy-wrapper.sh`, the runner setup, or the SSH surface
  triggers a security-reviewer pass (release-engineer.md).

---

## 7. Validation done for this WS

- `terraform fmt -check` + `terraform validate` (see the top-level task report).
- `shellcheck` on every `.sh` under `host/`.
- Manual consistency check: network names, container names, loopback ports, GHCR
  image refs, and the `active.caddy` symlink target line up across the compose
  files, the Caddyfile snippets, and `deploy.sh`.

**Not run** (Tim's authorized steps): `terraform plan`/`apply` against the real
`pve01`, and any real deploy.
