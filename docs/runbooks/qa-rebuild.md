# Runbook — Rebuild QA from scratch (and first-time QA bring-up)

QA is the **first** deploy target in the wave (ADR-0022): `deploy-qa → smoke-qa →
deploy-staging → …`. Like staging it is **disposable** — an LXC clone of the
staging pattern (VMID **8003**, `qa.proxcloud.lab`), single-stack, **no blue/green
and no rollback**. When it is wedged (bad migration state, corrupted volume,
drifted config) you do not repair it, you rebuild it. Its job is to catch a
deploy/config/migration regression on a cheap, safe-to-break environment before a
SHA reaches production-like staging (and, downstream, a reviewer's attention).

QA carries its own isolated state: `proxcloud-qa-*` containers, its own Postgres
(`proxcloud-qa-postgres`), its own compose project (`-p proxcloud-qa`), its own
smoke tenant/credentials, and its own reserved smoke VMID — so nothing it does can
touch staging or prod (ADR-0016 §4).

## Fast path — same guest, fresh stack

On the QA guest (`/opt/proxcloud`), destroy the stack and its data, then let the
next deploy rebuild it:

```bash
cd /opt/proxcloud
docker compose --env-file .env -p proxcloud-qa -f docker-compose.yml down -v
#   -v also drops the Postgres volume (proxcloud-qa-pgdata) — QA data is
#   throwaway. Keep .env; it is the manual, per-guest secret file.
```

Then re-run the last deploy (re-runs migrations from empty, re-seeds smoke):

```bash
# through the forced command, from CI/laptop:
ssh <qa-deploy-host> "deploy <last-known-good-SHA>"
# or on the guest:
/opt/proxcloud/bin/deploy.sh <SHA>
```

`deploy.sh` brings up Postgres, applies embedded migrations at backend boot (or
the migrator service if `USE_MIGRATOR_SERVICE=1`), runs `seed-smoke`
(`SMOKE_SEED=1`), then health-gates on `/api/v1/version == SHA`. Idempotent — safe
to re-run after a partial failure.

## Full path — reprovision the guest (also the first-time bring-up)

If the guest itself is broken (kernel/Docker-in-LXC issues, disk), or you are
standing QA up for the first time:

### 1. `terraform apply` the QA guest

```bash
cd deploy/terraform
export PROXMOX_VE_API_TOKEN='proxcloud@pve!tf=…'   # never in a file
terraform plan     # expect ONLY additive proxcloud_virtual_environment_container.qa
                   # + null_resource.qa_provision — no staging/prod diff
terraform apply    # Tim's authorized step; touches real pve01
```

The `qa` resource is the LXC clone (`deploy/terraform/qa.tf`); its sizing/network
twins live in `variables.tf` (`vmid_qa=8003`, `qa_cores/qa_memory/qa_disk_gb`,
`qa_static_ipv4_cidr`/`qa_gateway`, `qa_provision_host`). With DHCP + no DNS
registration, read the leased IP and set `qa_provision_host` before re-applying
(same caveat as staging — see `deploy/README.md` §1).

Provisioning re-runs `first-boot.sh` + `bootstrap.sh` (Docker, the locked `deploy`
user, the forced-command `authorized_keys`, the Postgres TLS cert). The **common**
scripts (`deploy/host/common/bin/*`, incl. the unchanged `deploy-wrapper.sh`
forced command) are reused as-is; only `deploy/host/qa/*` overlays the rest.

### 2. Create the `ci-deploy-qa` keypair and install its public half

QA gets its **own** least-privilege deploy key (never staging's or prod's). The
maintainer generates it once — Terraform does **not** create keys:

```bash
cd deploy/terraform/keys
ssh-keygen -t ed25519 -N '' -f ci-deploy-qa -C proxcloud-qa-deploy
#   private half  -> ci-deploy-qa      (git-ignored; becomes GitHub secret QA_SSH_KEY)
#   public  half  -> ci-deploy-qa.pub  (git-ignored; installed on the QA guest)
```

Install the **public** half by setting `qa_ci_deploy_public_key` in
`terraform.tfvars` to the contents of `ci-deploy-qa.pub` and re-applying (so
`bootstrap.sh` pins it into the deploy user's forced-command `authorized_keys`),
**or** drop `ci-deploy-qa.pub` at `/opt/proxcloud/ci-deploy-key.pub` on the guest
and re-run `sudo /opt/proxcloud/bootstrap.sh`. The private half **only** ever lives
as the GitHub repo secret `QA_SSH_KEY` — never in git, never on the runner image.

### 3. Create the scoped Proxmox token `proxcloud-qa@pve!cd`

QA talks to `pve01` with its own scoped API token (distinct from staging/prod), so
a QA credential can never act as another environment. On `pve01`:

```bash
pveum user add proxcloud-qa@pve            # if the user does not exist
pveum aclmod /pool/smoke -user proxcloud-qa@pve -role PVEVMAdmin   # or a custom role
pveum user token add proxcloud-qa@pve cd --privsep 0
#   -> prints the secret ONCE. It goes into /opt/proxcloud/.env as
#      PROXMOX_TOKEN_SECRET; it never enters git/CI.
```

The token needs **`Pool.Allocate`** on the smoke pool so `smoke-qa` can create and
delete its throwaway LXC (the same outstanding grant staging/prod need — ADR-0016
§4). Match the exact grants staging uses; QA is not more privileged.

### 4. Place `/opt/proxcloud/.env` by hand (never in git/CI)

App secrets never enter git or CI. On the QA guest, from the template shipped in
the tree:

```bash
cd /opt/proxcloud
cp env.example .env            # from deploy/host/qa/env.example
#   edit every CHANGEME: PROXMOX_TOKEN_SECRET, SECRETS_KEY (openssl rand -hex 32),
#   POSTGRES_PASSWORD (== the password inside DATABASE_URL!), SMOKE_PASSWORD, GHCR_TOKEN
sudo /opt/proxcloud/bootstrap.sh     # re-run: fixes .env perms (chmod 600, deploy-owned)
```

Ensure `SMOKE_EMAIL`/`SMOKE_PASSWORD` in `.env` **match** the GitHub `smoke-qa`
secrets (`QA_SMOKE_EMAIL`/`QA_SMOKE_PASSWORD`), or the smoke login will fail after
a rebuild.

### 5. GitHub configuration (repo-level — QA is ungated, like staging)

QA is **not** a protected Environment (only `production` is). Add these once at
`github.com/timkrebs/proxcloud` → Settings → Secrets and variables → Actions:

| Kind   | Name               | Value                                                     |
|--------|--------------------|-----------------------------------------------------------|
| secret | `QA_SSH_KEY`       | private half of `ci-deploy-qa` (from step 2)              |
| secret | `QA_SMOKE_EMAIL`   | `smoke@qa.proxcloud.lab` (matches `.env` `SMOKE_EMAIL`)   |
| secret | `QA_SMOKE_PASSWORD`| matches `.env` `SMOKE_PASSWORD`                            |
| var    | `QA_SSH_HOST`      | the QA guest host/IP (bare host; the job connects `-l deploy`) |
| var    | `QA_BASE_URL`      | `https://qa.proxcloud.lab` (or `http://…` behind cloudflared — see gotchas) |
| var    | `QA_SMOKE_VMID`    | **99003** — QA's reserved smoke VMID (distinct from staging/prod) |

Also **extend the shared `SSH_KNOWN_HOSTS`** secret to include the QA host so
`StrictHostKeyChecking=yes` stays on:

```bash
ssh-keyscan <staging-host> <qa-host> <prod-host>   # regenerate the WHOLE value
#   paste the combined output into the SSH_KNOWN_HOSTS repo secret
```

Reused **as-is** (already set for staging/prod, shared): the non-secret smoke
facts `SMOKE_TENANT`, `SMOKE_PROJECT`, `SMOKE_NODE`, `SMOKE_TEMPLATE`,
`SMOKE_STORAGE`, `SMOKE_BRIDGE`, plus `NTFY_URL`. `smoke-qa` overrides only the
env-specific ones (`QA_SMOKE_EMAIL/PASSWORD/VMID`, `QA_BASE_URL`).

### 6. Trigger the first deploy

```
Actions → deploy.yml → Run workflow → ref = <full-SHA on main>
```

or let a normal merge-to-`main` flow through publish → deploy. The first
`deploy-qa` builds the whole stack; `smoke-qa` then gates the wave before staging.

## Reserved smoke VMID (why 99003)

`smoke-qa` creates + deletes a throwaway LXC at a **reserved** VMID (ADR-0016 §4)
so it can never collide with real capacity or with another environment's smoke
LXC. QA uses **`QA_SMOKE_VMID = 99003`**, in ADR-0016's reserved `99000–99009`
smoke band, chosen to mirror the guest numbering (staging guest 8001 → smoke
`99001`, prod guest 8002 → smoke `99002`, QA guest 8003 → smoke **`99003`**). The
one hard rule: **it must not equal the staging or prod `SMOKE_VMID`.** If your
staging/prod smoke VMIDs use a different band, pick any free VMID that is distinct
from both and record it here.

## Gotchas QA inherits from RELEASING.md (don't re-debug staging)

- **Token id shape** — `PROXMOX_TOKEN_ID=proxcloud-qa@pve!cd` (user `@pve`, token
  name `cd`, `!` separator). A wrong realm/name here yields 401s that look like a
  code bug.
- **`PROXMOX_URL` by IP/hostname that resolves from the guest** — the QA `.env`
  ships `https://pve01.proxcloud.lab:8006`; if the guest can't resolve that, use
  the pve01 IP. `PROXMOX_TLS_INSECURE=true` for the homelab self-signed cert.
- **DB host is per-environment** — QA's `DATABASE_URL` host is
  **`proxcloud-qa-postgres`** (NOT `proxcloud-staging-postgres`), `sslmode=require`.
  The password in `DATABASE_URL` **must equal** `POSTGRES_PASSWORD`. A mismatch
  fails backend boot (and thus the health gate) before any smoke runs.
- **HTTP-vs-TLS base URL must match the Caddy mode** — `deploy/host/qa/caddy/Caddyfile`
  defaults to `tls internal` on `qa.proxcloud.lab` ⇒ `QA_BASE_URL=https://…`. If
  you switch the Caddyfile to the plain-`:80` cloudflared mode, `QA_BASE_URL` must
  become `http://…` (and `FRONTEND_ORIGIN` in `.env` must track it). A scheme
  mismatch makes the version/smoke checks fail on connection, not assertion.
- **Smoke-cred match** — the guest `.env` `SMOKE_EMAIL`/`SMOKE_PASSWORD` seed the
  user; the GitHub `QA_SMOKE_EMAIL`/`QA_SMOKE_PASSWORD` log in as it. They MUST be
  identical or `smoke-qa` fails at the login assertion (see `state/last-seed.log`).
- **`PROXCLOUD_ENV=production` + Postgres TLS on purpose** — QA mirrors prod's
  fail-closed config so a production-only regression is caught early. Do not
  "simplify" it to non-TLS; that defeats its purpose.

## Manual cross-file consistency audit (deploy/README.md §7 discipline)

Before/after a QA change, confirm these line up across the QA tree — a single
drifted name silently breaks a deploy or smoke:

- [ ] Compose project is `proxcloud-qa` everywhere it's invoked:
      `deploy/host/qa/bin/deploy.sh` (`-p proxcloud-qa`, all three `docker compose`
      call sites: `compose()`, the migrator `run`, the seed `run`).
- [ ] Container names all `proxcloud-qa-*` in `docker-compose.yml`
      (`-postgres/-backend/-frontend/-caddy`) and the volumes
      (`proxcloud-qa-pgdata`, `-caddy-data`, `-caddy-config`).
- [ ] Postgres host in `DATABASE_URL` (`env.example`) == the postgres
      `container_name` (`proxcloud-qa-postgres`), `sslmode=require`, and the
      password matches `POSTGRES_PASSWORD`.
- [ ] `Caddyfile` site address is `qa.proxcloud.lab` and its `reverse_proxy`
      targets are `proxcloud-qa-backend:8080` / `proxcloud-qa-frontend:3000`.
- [ ] Loopback ports are `127.0.0.1:8080` (backend) / `127.0.0.1:3000` (frontend)
      — the same single-stack ports `deploy.sh`'s health checks curl.
- [ ] `PROXMOX_TOKEN_ID=proxcloud-qa@pve!cd`, `FRONTEND_ORIGIN=https://qa.proxcloud.lab`,
      `SMOKE_EMAIL=smoke@qa.proxcloud.lab`, `SMOKE_SEED=1`, `USE_MIGRATOR_SERVICE=0`.
- [ ] The deploy workflow's QA smoke VMID (`vars.QA_SMOKE_VMID`) is distinct from
      staging's and prod's `SMOKE_VMID`.
- [ ] `deploy/host/qa/bin/deploy.sh` reuses `/opt/proxcloud/bin/deploy-wrapper.sh`
      UNCHANGED (the common forced command) — QA introduces no new SSH grammar.
