# ADR-0032: Installer-created Proxmox identity & least-privilege token model

Date: 2026-08-31 · Status: accepted · Security-critical · Delivery/Installer

## Context

The installer (ADR-0031) has to produce a working Proxmox credential without the
user reading a wiki. Today the README asks them to paste four `pveum` commands
(`README.md:56-67`) — and **that block is wrong in three ways**:

1. It grants **`VM.Monitor`**, which **does not exist on PVE 9** (`pveum role
   add` rejects it), so the copy-paste fails outright on a current host.
2. It **omits `Pool.Allocate`**, so ADR-0008's per-project pool creation — and
   the `pool=` parameter on every guest create — fails with 403.
3. It **omits `SDN.Use`**, which PVE 8.2+ requires to attach a guest NIC to a
   bridge, so guest creation fails on a modern host even though every `VM.*`
   privilege is present. It also omits `Datastore.AllocateTemplate` in practice
   for template/clone flows and predates the guest-agent privilege split.

It further uses `--privsep 0`, which sidesteps the single most important
configuration decision in this ADR rather than making it.

An installer that creates credentials on someone else's hypervisor is the most
security-sensitive thing this project does. The privilege set must be *exactly*
what Proxcloud calls, the token must be scoped as tightly as PVE allows, and the
result must be **verified against the live API** before the installer proceeds —
not assumed because `pveum` exited 0.

## Decision

### 1. One user, one role, one token — and the authoritative privilege set

The installer creates:

- user **`proxcloud@pve`** (PVE-realm, so no PAM/system account is touched),
- role **`Proxcloud`**,
- token **`proxcloud@pve!app`**.

**This ADR is the authoritative privilege list.** `README.md:56-67` is stale and
must be updated to match it; where the two disagree, this file wins.

```
VM.Audit VM.Allocate VM.Clone
VM.Config.CDROM VM.Config.CPU VM.Config.Cloudinit VM.Config.Disk
VM.Config.HWType VM.Config.Memory VM.Config.Network VM.Config.Options
VM.PowerMgmt VM.Console VM.Snapshot VM.Snapshot.Rollback
Datastore.Audit Datastore.AllocateSpace Datastore.AllocateTemplate
Sys.Audit Pool.Audit Pool.Allocate SDN.Use
```

plus **exactly one** version-dependent privilege, chosen from the parsed
`pveversion` major:

| PVE major | additional privilege | why |
| --- | --- | --- |
| 9 | `VM.GuestAgent.Audit` | PVE 9 split guest-agent access into `VM.GuestAgent.Audit` (read: `network-get-interfaces`, `get-osinfo`) and `VM.GuestAgent.Unrestricted` (arbitrary `exec`). `VM.Monitor` was removed. |
| 8 | `VM.Monitor` | Same capability under the old name; `VM.GuestAgent.*` does not exist yet. |

Passing the wrong one is a hard `pveum` error, so the installer must branch on
the detected major and must **fail preflight on an unrecognised major** rather
than guess.

**What each group buys, mapped to the client surface in
`backend/internal/proxmox`:**

- **`VM.Audit`, `VM.Allocate`, `VM.Clone`, `VM.PowerMgmt`** — the guest lifecycle:
  list/read guests, create (qemu + LXC), clone from a template, start/stop/
  reboot/shutdown.
- **`VM.Config.*`** — every field the create wizard and the resize/reconfigure
  paths write: CPU, memory, disk, NIC, HW type, boot options, the CD-ROM/ISO
  slot, and `Cloudinit` for the cloud-init drive and its inline parameters.
  These are separate privileges in PVE, and Proxcloud touches all of them, so all
  of them are granted; none is a placeholder.
- **`VM.Snapshot`, `VM.Snapshot.Rollback`** — the snapshot blade
  (`guest_config.go:191-251`: list, create, rollback, delete). Rollback is a
  distinct privilege from create and is required for the restore action.
- **`Sys.Audit` + `VM.Audit`** — metrics. Node status and `nodes/{node}/rrddata`
  (`gopve.go:338`) need `Sys.Audit`; per-guest `rrddata`
  (`guest_config.go:65`) needs `VM.Audit`. The SSE metrics stream is entirely
  read-only and lives on these two.
- **`Pool.Audit` + `Pool.Allocate`** — ADR-0008 mirrors every Proxcloud project
  as a Proxmox pool `pc-<tenant>-<project>` (`gopve.go:216-235`).
  `Pool.Allocate` is needed **twice**: to create/delete the pool, *and* because
  passing `pool=` on a guest create is itself a pool-allocation operation. This
  is the privilege whose absence produces the confusing "guest create works
  without a project, fails with one" symptom.
- **`Datastore.Audit`, `Datastore.AllocateSpace`, `Datastore.AllocateTemplate`** —
  enumerate storages, allocate disk volumes on create/resize/clone, and
  download/allocate templates and ISOs. `Datastore.AllocateTemplate` is what
  covers the template/ISO content types that `AllocateSpace` does not.
- **`SDN.Use`** — PVE 8.2+ checks bridge attachment against the SDN ACL path
  `/sdn/zones/localnetwork/<bridge>`, even for a plain Linux bridge with no SDN
  configured. Without it, every guest create fails at the NIC. This is the
  single most common "my token has everything and it still 403s" cause.
- **`VM.Console` (+ `VM.GuestAgent.Audit` / `VM.Monitor`)** — the console blade
  and the guest IP display (`guest_config.go:137`,
  `agent/network-get-interfaces`). Note ADR-0003: `VM.Console` makes the console
  *API* callable, but the websocket itself still rejects token auth — hence §4.

**Explicitly NOT granted**, and this list is as load-bearing as the one above:

- **`Sys.Modify`** — no host network/firewall/storage-definition changes.
- **`Permissions.Modify`** — the token cannot grant itself or anyone else
  anything. This is what makes the grant non-escalating.
- **`Datastore.Allocate`** — creating/removing *storage definitions* is a host
  administration act; Proxcloud only allocates *within* existing storages.
- **`Realm.Allocate` / `User.Modify` / any `Sys.*` beyond `Audit`** — no identity
  or host administration.
- **`VM.GuestAgent.Unrestricted`** — no arbitrary command execution inside
  guests. Proxcloud has no guest-exec path by design (ADR-0025, ADR-0028
  both record this), and the token must not be able to acquire one.
- **`VM.Backup`, `Sys.Console`, `Sys.PowerMgmt`** — no backup jobs, no host
  shell, no host reboot.

The installer **prints this exact list, plus a one-line "why", before it creates
anything**, and requires the user to accept. Nobody's hypervisor gets a new
credential from a script they did not see the scope of.
`install/README.md` documents it in full, with the deny-list.

### 2. Privilege separation on, ACL granted to **both** the user and the token

The token is created with **`--privsep 1`**, and the `Proxcloud` role is granted
at path `/` to **both** the user *and* the token:

```
pveum acl modify / --roles Proxcloud --users  proxcloud@pve
pveum acl modify / --roles Proxcloud --tokens 'proxcloud@pve!app'
```

**This is the classic PVE gotcha and the reason `--privsep 0` is tempting.** With
privilege separation on, a token's effective permissions are the **intersection**
of the user's permissions and the token's own ACL. Granting the role only to the
user leaves the token with an empty ACL — intersection empty — and *every* call
403s, with an error message that says nothing about ACLs. Granting only the token
fails the same way from the other side, and also breaks the console login (§4),
which authenticates as the *user*.

We keep `privsep 1` rather than take the README's `--privsep 0` shortcut because
privsep is what makes the token's scope independently revocable and independently
auditable: narrowing or revoking the token's ACL later cannot be silently undone
by the user's own permissions. The cost is exactly one extra `acl modify` line
and this paragraph.

Path is `/` (cluster-wide) in v1 because Proxcloud enumerates nodes, storages,
and bridges and creates pools — all of which live above any single pool's
subtree. Pool-scoped tokens become viable once every operation is pool-relative
(ADR-0008 §Consequences already flags this as the eventual direction); that is a
future ADR, not this one.

### 3. Idempotent convergence: modify the role, recreate the token

A re-run (ADR-0031 §9) must converge, not accumulate:

- **Role**: `pveum role add` if absent, otherwise **`pveum role modify … --privs
  <full set>`**. `role modify` replaces the privilege set wholesale, so it also
  *heals* a role a user previously created by hand from the stale README — the
  broken `VM.Monitor`-on-PVE-9 case repairs itself on the next installer run.
- **User**: created if absent; never deleted by a re-run.
- **Token**: if it exists, it is **removed and recreated**. A PVE token secret is
  shown exactly once at creation and is **unrecoverable by design**, so an
  installer that finds an existing token has no way to write a working `.env`
  except by minting a new secret. Recreation is announced in the output, because
  it invalidates the old secret — anything else still using
  `proxcloud@pve!app` (a hand-built deployment, another installer guest) stops
  working. Users with such a setup are told to run the installer with a distinct
  `PC_TOKEN_NAME`.

### 4. The console credential: a password, because tokens cannot do websockets

ADR-0003 records that Proxmox's `vncwebsocket`/`termproxy` endpoints reject
`PVEAPIToken` auth outright (go-proxmox surfaces this as
`ErrAPITokenWebSocketUnsupported`). So the console needs a **real login**, and the
installer gives the *same* `proxcloud@pve` user a generated password — set via
**stdin** (`pveum passwd proxcloud@pve` reading the password from a pipe, never
as an argv word) — and wires `PROXMOX_CONSOLE_USER` / `PROXMOX_CONSOLE_PASSWORD`
into the guest's `.env`.

Reusing the same identity rather than creating a second user is deliberate: the
console credential is bounded by the *same* least-privilege role (the ACL grant
to the user in §2 is what bounds it), and one identity means one thing to revoke.
The password never reaches the browser — the backend bridges the websocket and
issues a one-shot session id (ADR-0003).

### 5. Verification gate — the installer proves the credential works

`pveum` exiting 0 proves a command parsed, not that the token can do anything.
Before the installer writes a `.env` or starts a stack, it runs three checks and
**aborts with the real error** on any failure:

1. **ACL sanity** — `pveum user token permissions proxcloud@pve app --path /`
   returns a **non-empty** privilege set. This catches the privsep-intersection
   mistake (§2) immediately and by name.
2. **Live authoritative check** — a real HTTPS call with the *actual token
   string* against `https://127.0.0.1:8006/api2/json/version` and
   `/api2/json/cluster/resources`, both asserted 2xx. This is the only check that
   proves the end-to-end credential; §1 is only as good as what PVE actually
   enforces. The `Authorization: PVEAPIToken=…` header is supplied to `curl`
   **via a config file on stdin (`curl --config -`)**, so the secret never
   appears in `argv`, in `/proc`, or in the host's shell history.
3. **Console login check** — `POST /api2/json/access/ticket` with the user +
   generated password returns a ticket. If this fails the installer does **not**
   abort the whole run: it warns, leaves `PROXMOX_CONSOLE_*` unset, and the
   Console blade renders its honest disabled state (ADR-0003). A broken console
   should not cost the user their whole install.

The loopback address is used deliberately: the installer runs on the PVE host, so
`127.0.0.1:8006` needs no DNS, no external reachability, and no certificate
trust decision — TLS verification is skipped for this loopback probe only, and
the guest's `.env` gets `PROXMOX_TLS_INSECURE` set according to the URL the user
chose, not according to this probe.

### 6. Secret-handling iron rules for the installer

- The token secret, the console password, the Postgres password, `SECRETS_KEY`,
  and the admin password exist **only** as shell variables in the running
  installer and in the guest's **0600 `.env`**.
- **Never on `argv`.** Every command that consumes a secret reads it from
  **stdin** (`pveum passwd`, `curl --config -`). `ps` on the PVE host must never
  show a Proxcloud secret.
- **Never in the install log.** The installer `tee`s its output to a log file;
  `run()` is **redaction-aware** — it holds the set of known secret values and
  replaces them with `***` in anything it echoes, so a secret cannot reach the
  log even through a command's own error output.
- **Never in the summary**, with exactly one exception: the generated **admin
  password is printed once directly to the tty**, bypassing the `tee` (ADR-0031
  §8). The Proxmox token secret is *never* printed — it is only written to the
  guest `.env`.
- Secrets are generated with `openssl rand` (`SECRETS_KEY` as 32 bytes hex, per
  the documented `openssl rand -hex 32`), never from `$RANDOM`, a timestamp, or
  a host fingerprint.
- The host-side staging directory is `mktemp -d` at mode 0700 and is removed by
  an `EXIT` trap on every path, success or failure.

## Consequences

- The privilege list in this ADR becomes the single source of truth for what
  Proxcloud needs from Proxmox. `README.md:56-67` must be corrected to match
  (drop `VM.Monitor` on PVE 9, add `Pool.Allocate`, `SDN.Use`,
  `Datastore.AllocateTemplate`, `VM.GuestAgent.Audit`); leaving it stale means
  the installer and the manual path produce differently-broken deployments.
- Any new Proxmox capability added to `backend/internal/proxmox` may need a new
  privilege here. Adding one without updating this list produces a 403 that only
  appears on installer-created deployments (where the token is tight) and not on
  a developer's `--privsep 0` token — a nasty class of bug. Treat "does this need
  a privilege?" as part of adding a client method.
- `privsep 1` with a dual ACL grant is two commands where one would "work". The
  payoff is that the token's scope is independently revocable and cannot be
  widened by the user's permissions drifting; the failure mode of getting it
  wrong is caught by verification check 1, by name, in the same run.
- Token recreation on re-run invalidates any other consumer of
  `proxcloud@pve!app`. This is unavoidable (PVE will not reveal an existing
  secret) and is therefore announced loudly rather than silently survived.
- A hostile or compromised Proxcloud instance can create, reconfigure, snapshot,
  and destroy guests and pools, and read cluster state. It **cannot** modify host
  configuration, grant permissions, create storages or users, run commands inside
  guests, or reach the host shell. That boundary is the deny-list in §1 and it is
  the security claim the installer makes to its users.
- Console availability depends on a password credential living in the guest's
  `.env`. That is a real second credential (ADR-0003's tradeoff), now created
  automatically rather than by hand — so the installer must be equally explicit
  that it exists, and `uninstall.sh --purge-pve-objects` must remove the user, not
  just the token.

## Alternatives considered

- **`--privsep 0`** (the current README). Rejected: it makes the token exactly as
  powerful as the user with no independent scope, so narrowing or auditing the
  token separately is impossible, and a later widening of the user's ACL silently
  widens the token. The only thing it saves is one `acl modify` line — and it
  hides the intersection semantics that every operator debugging a 403 needs to
  understand anyway.
- **`root@pam` or an `Administrator`-role token.** Rejected outright: an
  installer that takes root on a stranger's hypervisor to save a `pveum role add`
  is indefensible, and it would make the deny-list in §1 empty.
- **A pool-scoped ACL** (grant on `/pool/pc-*` instead of `/`). Rejected for v1
  as not yet achievable: Proxcloud enumerates nodes, storages, and bridges
  (`Sys.Audit`, `Datastore.Audit`, `SDN.Use`) and *creates* pools, none of which
  live under a pool subtree. Recorded as the direction ADR-0008 anticipates,
  behind an operation-by-operation audit.
- **Two identities — a token user and a separate console user.** Rejected: two
  passwords, two ACLs, two things to revoke, and two ways for the privilege sets
  to drift apart, for no additional confinement (both would carry the same role).
- **Skipping the live API check** and trusting `pveum` exit codes. Rejected: the
  privsep-intersection failure is *invisible* to `pveum` — every command succeeds
  and the token is inert. Without check 2 the installer would happily hand the
  user a stack that 403s on its first click, which is precisely the
  fabricated-success failure iron rule 5 forbids.
- **Passing the token secret to `curl -H`** instead of a stdin config. Rejected:
  it puts the secret in `argv`, visible to any user on the PVE host via `ps`, for
  the duration of the call.
- **Reusing an existing token if one is found.** Rejected on capability grounds:
  PVE does not disclose an existing token's secret, so there is nothing to reuse.
  Recreate-and-announce is the only honest option.
- **Granting `VM.GuestAgent.Unrestricted`** to enable future in-guest automation.
  Rejected: it is arbitrary remote code execution inside every guest, granted
  pre-emptively for a capability the backend deliberately does not have
  (ADR-0025, ADR-0028). If a guest-exec feature is ever built, it gets its own
  ADR and its own explicit consent step.

See ADR-0031 (the installer that creates and consumes this identity),
ADR-0033 (how the installer payload itself is verified before it is allowed to
run `pveum` as root), ADR-0003 (why the console needs the password credential in
§4), ADR-0008 (project→pool mirroring, the reason `Pool.Allocate` and
`Pool.Audit` are granted), and ADR-0014 (the private CD path, whose tokens are
provisioned by hand and are out of this ADR's scope).
