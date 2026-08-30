# Runbook — Enable the service catalog LIVE in QA

Scope: turn on the **service catalog** (ADR-0025/0026/0028) and, optionally, the
**K3s deployment sets** (ADR-0029/0030) on the **QA** guest only. The catalog code
is merged but **dormant** (`CATALOG_ENABLED=false` everywhere). Enabling it is an
**operator procedure, not a code change** — you create a dedicated least-privilege
SSH writer on the Proxmox node, pin its host key, place the credentials on the QA
guest, grant one extra token privilege, then flip the flag and redeploy.

**Degrade-safety (why this is low-risk):** the compose mount and the env block are
inert until `CATALOG_ENABLED=true`. Even after the flip, if the snippet writer
cannot initialize (key missing/unreadable, bad `known_hosts`), the backend logs
loudly and **keeps serving** — catalog provisioning returns `503`, everything else
(auth, resources, SSE) stays up. A mistake here does not take QA down.

## Facts (do not re-derive)

| Thing | Value |
|---|---|
| QA guest (LXC) | `192.168.1.22`, deploy root `/opt/proxcloud` |
| Proxmox node | `pve01`, SSH host `192.168.1.128` |
| QA API token | `proxcloud-qa@pve!cd`, role `Proxcloud`, granted at `/`, `--privsep 0` |
| Backend container UID | **65532** (distroless `nonroot`, `backend/Dockerfile`) |
| Snippet datastore | `local` → node path `/var/lib/vz/snippets` |
| Creds dir on QA guest | `/opt/proxcloud/snippet-writer/` (bind-mounted `:ro` into backend at `/etc/proxcloud/snippet-writer`) |
| Snippet SSH user (to create) | `proxcloud-snippets` on pve01, **no login shell**, SFTP-only |

Ordering rule: the **compose mount and env keys are harmless before the key
exists**; the **flag flip is what activates the feature**. Do steps 0–5 first, then
6–7. You can land the compose change on `main` and let it auto-deploy at any time
before step 6 — an empty `snippet-writer/` dir just means provisioning returns 503
until you finish.

---

## Step 0 — [pve01] Enable `snippets` content on the `local` datastore

`cicustom` references `local:snippets/<file>`. By default the `local` storage's
content list is `iso,vztmpl,backup` — **`snippets` is not enabled**, and `qm set
--cicustom` will reject the reference until it is. Append it (preserve the existing
list; check first):

```bash
# [pve01] as root — see the current content types:
grep -A6 '^dir: local' /etc/pve/storage.cfg
# Append 'snippets' to whatever is already there, e.g. if it shows
#   content iso,vztmpl,backup
pvesm set local --content iso,vztmpl,backup,snippets
mkdir -p /var/lib/vz/snippets    # PVE also creates it, but be explicit
```

---

## Step 1 — [pve01] Create the dedicated least-privilege SSH writer

Create a system user with **no login shell and no sudo**, whose only capability is
writing to the snippet directory. The write grant is a **group + setgid**
ownership of `/var/lib/vz/snippets` — nothing else on the node is granted.

```bash
# [pve01] as root
useradd --system --create-home --home-dir /home/proxcloud-snippets \
        --shell /usr/sbin/nologin --user-group proxcloud-snippets

# Least-priv write grant: group-own the snippet dir by the new user's group and
# set setgid so files the writer creates inherit that group. This is the ONLY
# filesystem privilege the user gets.
chgrp proxcloud-snippets /var/lib/vz/snippets
chmod 2775 /var/lib/vz/snippets
```

**Assumption stated plainly:** this grants group-write on exactly one directory
(`/var/lib/vz/snippets`) and nothing else; Proxmox itself reads snippets as root at
guest start, so it is unaffected by the group ownership. If your `local` datastore
lives somewhere other than `/var/lib/vz`, use that path instead (check
`/etc/pve/storage.cfg`). No `chroot` is used (see Step 2 for why).

## Step 2 — [pve01] SFTP-only access, no login shell

Because the user has `nologin`, sshd would refuse to run the SFTP subsystem the
normal way (a subsystem is exec'd via the login shell). The correct, hardened fix
is an in-process SFTP server forced for this one user. Append a `Match` block at
the **end** of `/etc/ssh/sshd_config` (Match blocks must be last):

```
# --- Proxcloud snippet writer: SFTP-only, no shell, no forwarding ---
Match User proxcloud-snippets
    ForceCommand internal-sftp
    AllowTcpForwarding no
    AllowAgentForwarding no
    X11Forwarding no
    PermitTTY no
```

```bash
# [pve01] validate config BEFORE reloading, then reload (never restart — a bad
# config that fails to reload leaves the running sshd untouched):
sshd -t && systemctl reload ssh    # 'sshd' on some distros
```

**Why no `ChrootDirectory`:** the backend writes to the **absolute** node path
`/var/lib/vz/snippets` (config `SNIPPET_STORAGE_PATH`). A chroot would rewrite the
filesystem root and break that absolute path. Confinement here is by the
write-grant (Step 1) plus `ForceCommand internal-sftp` (no shell, no exec, no
forwarding). Reads are limited to world-readable files — acceptable for a homelab
node; the writer is nologin and key-only.

## Step 3 — [qa-guest] Generate the writer keypair

Generate the keypair **on the QA guest** so the private key never travels. It has
**no passphrase** (it is an unattended automated writer — a passphrase it cannot
supply would just wedge the writer).

```bash
# [qa-guest] as root — the dir already exists (created by bootstrap.sh, 0500/65532)
ssh-keygen -t ed25519 -N '' -C 'proxcloud-snippets@qa' \
           -f /opt/proxcloud/snippet-writer/id_ed25519
cat /opt/proxcloud/snippet-writer/id_ed25519.pub    # copy this line for Step 4
```

## Step 4 — [pve01] Install the public key (with hardening)

Put the public key in the writer's `authorized_keys`, pinned to the QA guest's
source IP and stripped of everything but SFTP. `restrict` disables pty and all
forwarding; `from=` refuses the key from any host but QA. Combined with the
`ForceCommand internal-sftp` from Step 2, this key can do exactly one thing from
exactly one host.

```bash
# [pve01] as root — replace <PUBKEY> with the line printed in Step 3
install -d -m 700 -o proxcloud-snippets -g proxcloud-snippets /home/proxcloud-snippets/.ssh
printf 'from="192.168.1.22",restrict %s\n' '<PUBKEY>' \
  >> /home/proxcloud-snippets/.ssh/authorized_keys
chown proxcloud-snippets:proxcloud-snippets /home/proxcloud-snippets/.ssh/authorized_keys
chmod 600 /home/proxcloud-snippets/.ssh/authorized_keys
```

## Step 5 — [qa-guest] Pin the node host key, place credentials, set ownership

Host-key verification is **mandatory** in the code (`knownhosts.New`, no insecure
fallback — ADR-0025). Pin pve01's host key into `known_hosts`; do **not** skip it.

```bash
# [qa-guest] as root — pin pve01's ed25519 host key (verify the fingerprint out of
# band against `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on pve01):
ssh-keyscan -t ed25519 192.168.1.128 > /opt/proxcloud/snippet-writer/known_hosts
```

Now set ownership so the **distroless backend (UID 65532)** can read the key. This
mirrors the postgres `./tls` precedent (which uses `70:70` for the postgres
container UID). The container UID and host UID are the same number (no userns
remap), so a file owned by host `65532` is readable by the in-container `nonroot`:

```bash
# [qa-guest] as root
rm -f /opt/proxcloud/snippet-writer/id_ed25519.pub    # public half not needed on the guest
chown 65532:65532 /opt/proxcloud/snippet-writer/id_ed25519 \
                  /opt/proxcloud/snippet-writer/known_hosts
chmod 400 /opt/proxcloud/snippet-writer/id_ed25519
chmod 440 /opt/proxcloud/snippet-writer/known_hosts
ls -ln /opt/proxcloud/snippet-writer    # expect owner/group 65532 on both files
```

**Why this is the #1 gotcha:** the backend runs as UID 65532 with no ability to
escalate. A key that is `root:root 0400` (the ssh-keygen default) is **unreadable**
to it — the writer fails to init and provisioning silently returns 503. It MUST be
`65532:65532` and readable by that UID.

## Step 6 — [qa-guest] Grant `VM.GuestAgent.Audit` on the token role

The catalog `configuring` step reads the guest agent's IP
(`agent/network-get-interfaces`) to prove the OS booted before probing the service
port. On PVE 9 that endpoint requires **`VM.GuestAgent.Audit`** (the replacement
for the now-invalid `VM.Monitor`). The `Proxcloud` role already has
`VM.Config.Cloudinit`, `SDN.Use`, and `Pool.Allocate` — do **not** re-grant those.
**Append** the one missing privilege:

```bash
# [pve01] as root — --append 1 adds to the role WITHOUT replacing the existing privs
pveum role modify Proxcloud --privs "VM.GuestAgent.Audit" --append 1
pveum role list --output-format json | \
  python3 -c 'import sys,json; r=[x for x in json.load(sys.stdin) if x["roleid"]=="Proxcloud"][0]; print("VM.GuestAgent.Audit" in r["privs"])'
# expect: True
```

Because the token is `--privsep 0`, the role change propagates to
`proxcloud-qa@pve!cd` automatically — no token change needed. If you skip this, the
readiness probe never sees an IP and the deployment fails honestly with
`guest booted but no routable IP appeared within <timeout>` (see Step 8).

## Step 7 — [qa-guest] Set the catalog env block and flip the flag

Edit `/opt/proxcloud/.env` (the manual, per-guest secret file — never in git). Add
the catalog block from `deploy/host/qa/env.example`, matching the container paths
to the mount, and set the flags **on**:

```ini
CATALOG_ENABLED=true
DEPLOYMENT_SETS_ENABLED=true          # only if you want the K3s cluster action
PROXMOX_NODE_SSH_HOST=192.168.1.128
PROXMOX_NODE_SSH_USER=proxcloud-snippets
PROXMOX_NODE_SSH_KEY_PATH=/etc/proxcloud/snippet-writer/id_ed25519
PROXMOX_NODE_KNOWN_HOSTS=/etc/proxcloud/snippet-writer/known_hosts
SNIPPET_DATASTORE=local
SNIPPET_STORAGE_PATH=/var/lib/vz/snippets
```

## Step 8 — [qa-guest] Redeploy / restart the backend, then verify

The compose mount is picked up on the next `up`. The flag is read at backend boot,
so the backend must be (re)created after the `.env` edit.

```bash
# [qa-guest] recreate the backend against the current SHA. Simplest is to re-run
# the last deploy through the forced command (idempotent, ADR-0022):
/opt/proxcloud/bin/deploy.sh <last-known-good-SHA>
# or, to just recreate the backend in place with the new env + mount:
cd /opt/proxcloud
docker compose --env-file .env -p proxcloud-qa -f docker-compose.yml up -d --force-recreate backend
```

Verify, in order:

1. **Boot log says enabled** (not degraded):
   ```bash
   docker logs proxcloud-qa-backend 2>&1 | grep -i 'service catalog'
   ```
   Expect: `service catalog enabled  services=<N> snippet_datastore=local ssh_host=192.168.1.128`.
   If instead you see `catalog provisioning disabled — the snippet writer could
   not be initialized`, the key is unreadable/missing or `known_hosts` is bad —
   re-check Step 5 (ownership 65532, mode 400) and Step 3 (key present). The
   backend stays up either way.

2. **Provision a PostgreSQL from the UI**: All resources → Create → the catalog
   PostgreSQL service. The task should walk `provisioning → configuring → ready`.
   `configuring` is the step that waits for the guest agent IP, then TCP-probes
   `5432`. On `ready`, the deployment's **Connection** shows `<ip>:5432` and the
   **Next steps** panel shows a connection string + a credential hint (the hint,
   never the secret).

3. **Confirm the snippet actually landed on the node** (proof the SFTP path
   worked):
   ```bash
   # [pve01]
   ls -l /var/lib/vz/snippets/proxcloud-*.yaml
   ```
   Files are named `proxcloud-<name>.yaml` (validated allowlist). A completed
   deployment removes its snippet again on teardown; a failed `configuring` step
   also removes it.

4. **Connect** with the surfaced coordinates:
   ```bash
   psql "postgres://<user>@<ip>:5432/<db>"    # <user>/<db>/password per the Next-steps panel
   ```

**Reading an honest failure** (the pipeline never fabricates readiness):
- `guest booted but no routable IP appeared within <timeout>` → the agent never
  reported an IP. Usual causes: `VM.GuestAgent.Audit` not granted (Step 6 — you'll
  also see repeated 403 debug lines for `agent/network-get-interfaces`), or the
  guest's qemu-guest-agent isn't running.
- `guest booted at <ip> but port 5432 never became reachable within <timeout>` →
  the OS booted but the service never opened its port (cloud-init failure inside
  the guest). Inspect the guest's console / cloud-init logs.
- `503 catalog provisioning is unavailable: snippet writer is not configured` at
  create time → the writer degraded at boot (see verify step 1).

## Step 9 — If the guest `.env` is Terraform-rendered

If `/opt/proxcloud/.env` on the QA guest is rendered by Terraform (a `templatefile`
under `deploy/terraform/`), **also add these keys to the tfvars / template** — the
next `terraform apply` will otherwise overwrite the file and revert
`CATALOG_ENABLED` to `false`, silently disabling the catalog on the next deploy.
Set them in the same place the other QA env values are managed, then re-render.

## Step 10 — Rollback (instant, no code change)

To disable the catalog immediately, flip one line in `/opt/proxcloud/.env` and
recreate the backend:

```bash
# [qa-guest]
sed -i 's/^CATALOG_ENABLED=true/CATALOG_ENABLED=false/' /opt/proxcloud/.env
docker compose --env-file .env -p proxcloud-qa -f docker-compose.yml up -d --force-recreate backend
```

The catalog routes still mount (completeness tests) but List/Get return the feature
as off and provisioning is unavailable. No credentials need to be removed to
disable it; leave the `snippet-writer/` dir and the pve01 user in place for the
next enable. To fully de-provision, additionally remove
`/home/proxcloud-snippets/.ssh/authorized_keys` on pve01 and delete
`/opt/proxcloud/snippet-writer/id_ed25519`.
