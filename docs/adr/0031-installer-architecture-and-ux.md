# ADR-0031: One-command public installer — architecture & UX

Date: 2026-08-31 · Status: accepted · Delivery/Installer

## Context

Proxcloud has exactly one working install path today, and it is **private**:
Terraform lays down guests on `pve01`, `bootstrap.sh` seeds `/opt/proxcloud`, and
CD pushes GHCR images by SHA over a forced-command SSH channel
(ADR-0014, ADR-0015, ADR-0022). Every piece of it assumes Tim's host, Tim's
secrets, and a self-hosted runner. A stranger with a Proxmox VE box has nothing
but the README quick start: clone the repo, hand-build a Proxmox role and token
(`README.md:56-67`), hand-write a `.env` with `SECRETS_KEY`, and hope
`docker-compose up --build` matches whatever the code expects this week. That is
a 30-minute, error-prone, undocumented-failure-mode experience for a product
whose entire value proposition is "self-service".

The Proxmox community has a de-facto convention for this — community-scripts.org
(formerly tteck's scripts): a single line pasted into the PVE host shell,
`bash -c "$(curl -fsSL …)"`, which asks a few questions and hands back a URL.
Users of Proxmox already recognise and trust that shape. We adopt it.

**Hard boundary.** This is a *parallel* delivery path. The private prod pipeline
— `deploy/`, `ci.yml`, `publish.yml`, `deploy.yml`, `soak.yml` — is not modified
by this wave. The installer shares *conventions and logic patterns* with
`deploy/host/qa` (compose shape, Postgres TLS recipe, health gating), never
files, credentials, or workflow steps. Two paths, one set of habits.

This ADR fixes the installer's architecture and its user experience. ADR-0032
fixes the Proxmox privilege model it creates; ADR-0033 fixes how the installer
itself is packaged, versioned, and verified.

## Decision

### 1. Two layers: a thin bootstrap in the repo root, a versioned payload

Repo root `install.sh` is a ~120-line, **version-agnostic** bootstrap: it
resolves the release, downloads and **checksum-verifies** the payload tarball,
and execs the orchestrator inside it. It carries no per-release data, so the
`main`-branch copy the one-liner fetches can never drift from the release it
installs. The full mechanism, its trust chain, and the release plumbing are
ADR-0033.

The payload is `install/`, and its orchestrator `install/install.sh` sources
six libraries with one job each:

| file | responsibility |
| --- | --- |
| `install/lib/core.func` | logging, colours, the redaction-aware `run()` wrapper, `render()`, input validators, abort/cleanup traps |
| `install/lib/preflight.func` | root check, PVE version + `pveversion` parse, free VMID, storage/bridge discovery, RAM/disk headroom, network reachability |
| `install/lib/guest.func` | template download, `pct create`, start, wait-for-network, Docker install |
| `install/lib/token.func` | user/role/token/ACL creation + the verification gate (ADR-0032) |
| `install/lib/stack.func` | `.env` generation, compose render + push, migrate, up, health gate |
| `install/lib/summary.func` | the final panel, credential reveal, state-file write |

Small files with one responsibility each are reviewable by a stranger who is
about to run them as root, which is the entire point.

### 2. Two modes, plus env overrides and an unattended path

- **Default mode** — the community-scripts default: confirm, then a handful of
  prompts (storage, bridge) with everything else preset. Target: under 60
  seconds of human input.
- **Advanced mode** — every knob: VMID, hostname, cores/RAM/disk, static IP or
  DHCP, storage, bridge/VLAN, TLS mode, admin username, image tag.
- **`PC_*` environment overrides** for every prompt (`PC_VMID`, `PC_STORAGE`,
  `PC_BRIDGE`, `PC_CORES`, …), and **`PC_YES=1`** for a fully unattended run
  that takes defaults and never blocks on a prompt. This makes the installer
  scriptable (and testable in CI, ADR-0033) without a second code path — the
  prompt function simply returns the override when one is set.

### 3. Guest shape: an unprivileged LXC with `nesting=1,keyctl=1`

Default guest is an **unprivileged LXC container**, created with:

- `--features nesting=1,keyctl=1` — **both** are required. Docker inside an
  unprivileged container needs `nesting` to permit the nested cgroup/mount
  namespaces *and* `keyctl` because containerd's use of the kernel keyring fails
  in an unprivileged container without it. Nesting alone is the classic
  half-configured state that gets you a container where `dockerd` starts and
  then dies on first pull. Both, or neither.
- Newest `debian-12-standard` template, resolved from `pveam available` and
  downloaded with `pveam download` when absent (never a hardcoded filename that
  rots on the next point release).
- Defaults: **2 vCPU / 4096 MB / 32 GB / DHCP**, `--onboot 1`, `--tags proxcloud`.
- The `proxcloud` tag is load-bearing: it is how a re-run finds a guest it
  previously created (§8) and how `uninstall.sh` refuses to touch a guest it did
  not create.

**Unprivileged, not privileged**, because a privileged container running Docker
is a root-equivalent handle on the host, and we would be asking strangers to
accept that with a one-liner. **LXC, not a VM**, because an LXC boots in seconds,
costs no nested-virtualisation support, and needs no cloud-init datasource on the
target host. **A VM guest type is deferred to a later phase** and documented as
such — the guest-creation seam in `guest.func` takes the guest type as a
parameter so adding `qemu` later is an added branch, not a rewrite.

### 4. Host-driven transport: `pct push` + `pct exec`, never SSH

The installer already runs **as root on the PVE host**. It therefore reaches into
the guest the way root on the host already can:

- Files are rendered on the host into a `mktemp -d` staging directory with mode
  **0700**, then delivered with **`pct push`**.
- Commands run as discrete **`pct exec`** steps, each wrapped by `run()` so a
  failure names the step ("Docker install failed", "compose up failed"), captures
  the tail of its output, and aborts with that message rather than a bare
  `set -e` exit.

**No SSH into the guest.** SSH would mean generating or accepting a keypair,
installing `sshd`, opening a port, and managing an `authorized_keys` file — a new
credential surface and a new listening service, to gain a capability we already
have for free. (Contrast ADR-0025, where SSH to the *node* is unavoidable because
the backend has no other way to write a snippet; there, the trust surface buys a
capability. Here it buys nothing.) This is also the one structural difference
from `deploy/host/*/bootstrap.sh`, which must use SSH because CI is remote.

### 5. The guest runs the stack as root — a deliberate deviation

`deploy/host/common/bin/first-boot.sh:26-33` creates a locked, key-only `deploy`
user in the `docker` group and runs compose as that user. The installer's guest
**runs the stack as root inside the container**, and that is intentional:

1. **No remote push target.** The `deploy` user exists so a CI forced-command has
   a non-root identity to land on (ADR-0015). Nothing pushes to an installer
   guest — updates are pulled by the in-guest `proxcloud` helper (§9).
2. **The container is the isolation boundary.** An unprivileged LXC's root is not
   the host's root; the boundary that matters is the container wall, and it is
   already there.
3. **`docker` group membership is root-equivalent anyway** — `deploy/README.md`
   already records this. A `deploy` user in the `docker` group buys ceremony, not
   privilege separation.

The deviation is recorded here so nobody "fixes" it later by copying
`first-boot.sh` wholesale.

### 6. Templates are files with `%%VAR%%` placeholders and a strict `render()`

Every generated artefact — `.env`, `docker-compose.yml`, `Caddyfile`, the
`proxcloud` helper — lives in `install/templates/` as a real file containing
`%%PLACEHOLDER%%` tokens. `render()` substitutes them and **refuses to substitute
a value that has not been through a validator**, aborting rather than emitting a
half-rendered file. Complementary rules:

- **Validate at intake, not at use.** Every user-supplied value is checked the
  moment it is read: hostname/VMID/CIDR by regex, storage by membership in the
  live `pvesm status` output, bridge by membership in the actual bridge list. A
  value that reaches `render()` has already been proven to be in an allowlist or
  to match a regex; interpolation is never the first line of defence.
- **All `pct`/`pveum`/`pvesm` invocations are argv arrays**, never a string
  passed to a shell. There is no path by which a prompt answer becomes a word in
  a command line.
- Heredocs generating config inline are banned — they make the eventual
  file invisible to review and to `shellcheck`.

### 7. Stack shape and the Caddy / origin / cookie chain

The generated stack is an adaptation of `deploy/host/qa/docker-compose.yml`:
**postgres** (TLS on, cert generated in-guest by the `gen-postgres-cert.sh`
recipe), **backend**, **frontend**, a **`migrate` profile** one-shot migrator,
and **caddy**. Postgres TLS is not optional polish: the backend fails closed when
`PROXCLOUD_ENV=production` and `DATABASE_URL` is not TLS
(`deploy/host/common/bin/gen-postgres-cert.sh:6-7`), so `sslmode=require` plus a
self-signed in-guest cert (owned uid 70, key mode 600) is the minimum that boots.

The default web front is deliberately the **plain-HTTP** one, and the three
settings below are one interlocking decision, not three:

1. **Caddy listens on `:80` with a site block that matches any Host.** A fresh
   installer user has an IP, not a DNS name; a Caddyfile keyed to a hostname
   would 404 the very URL we print.
2. **`FRONTEND_ORIGIN=http://<guest-ip>`, byte-equal to the printed URL.**
   `originCheck` compares `scheme://host` with `strings.EqualFold` against
   `FRONTEND_ORIGIN` (`backend/internal/httpserver/router.go:199-202`) — an exact
   match, no prefix or suffix tolerance. The installer therefore derives the
   printed URL and `FRONTEND_ORIGIN` from the *same* variable, so they cannot
   diverge. (Consequence: browsing the portal by a hostname that is not
   `FRONTEND_ORIGIN` fails mutations with a 403 until `proxcloud reconfigure`
   updates it. This is stated in the summary panel and in `install/README.md`.)
3. **Cookies come out correctly non-`Secure` without any insecure flag.** Caddy's
   `reverse_proxy` sets `X-Forwarded-Proto` automatically;
   `TRUST_PROXY_HEADERS` defaults to on in production
   (`backend/internal/config/config.go:177`, `envBool("TRUST_PROXY_HEADERS", !cfg.Dev)`),
   so the session layer reads the forwarded scheme and issues a non-`Secure`
   cookie over plain HTTP (`backend/internal/auth/session.go:80-92`).
   **`PROXCLOUD_INSECURE_COOKIES` is never set by the installer** — it is fatal
   under `PROXCLOUD_ENV=production` (`config.go:239`), and reaching for it would
   mean either a crash-looping backend or downgrading the whole deployment out of
   production mode. The plain-HTTP default works *because* the proxy chain is
   honest about the scheme, not because a safety was disabled.

Advanced mode offers `tls internal` (Caddy's local CA) or a bring-your-own
certificate; both flip the printed URL, `FRONTEND_ORIGIN`, and the Caddyfile
together, from the same single source of truth.

### 8. Admin bootstrap: seed once, reveal once

The generated `.env` carries `ADMIN_USER=admin` and a generated
`ADMIN_PASSWORD`. `SeedEnvAdmin` (`backend/cmd/proxcloud/main.go:123`) acts
**only when the users table is empty** and is otherwise a no-op, so it is safe on
every restart and every update; a bare `admin` is normalised to the login
**`admin@proxcloud.local`**, which is what the summary prints.

The generated password is **printed once, to the tty**, deliberately bypassing
the `tee`'d install log so it never lands in a file the user forgets about. It is
never re-derivable: a lost password is a `proxcloud reconfigure`, not a lookup.

### 9. Idempotent re-run: a host-side state file plus the guest tag

`/etc/proxcloud-installer.conf` (mode **0600**, on the host, **containing no
secrets** — VMID, hostname, IP, installer/app version, storage, bridge, TLS mode,
install timestamp) is the source of truth for "is Proxcloud already installed
here?". The guest's `proxcloud` tag is the corroborating signal.

On a re-run the installer presents **Update / Reconfigure / Reinstall / Cancel**.
The adopt-or-abort rule closes the dangerous middle ground: state file present
and the guest exists and carries the tag → adopt it; state file present but the
guest is gone → offer a clean reinstall; a guest occupying the recorded VMID
**without** the tag → abort loudly rather than touch someone else's container.
The installer never silently reuses or destroys a guest it cannot prove it made.

### 10. Helper in the guest, uninstaller on the host

- **`proxcloud`** — a CLI installed *inside the guest* (`update`, `status`,
  `logs`, `version`, `reconfigure`). It pulls a new pinned image set, runs the
  migrator, restarts the stack, and re-runs the health gate. It lives in the
  guest because that is where the compose project, the `.env`, and the container
  logs are.
- **`uninstall.sh`** — *host-side only*. A container cannot destroy itself, and
  `pveum` (removing the token, user, and role) does not exist inside it. It is
  guarded by a **typed-name confirmation** (the user types the guest hostname),
  removes the guest and the state file, and only with an explicit
  `--purge-pve-objects` also removes the `proxcloud@pve` user, token, and
  `Proxcloud` role (ADR-0032) — because those may be shared with a hand-built
  deployment the installer did not create.

### 11. The health gate is the definition of success

The installer does not print a success panel because `compose up` returned 0. It
polls the backend's health endpoint and asserts a real HTTP 200 through Caddy,
with a bounded timeout. On failure it prints the failing container's log tail and
the exact `pct exec … docker compose logs` command to re-read it. Iron rule 5
(honest task states) applies to the installer itself: no fabricated "Success!".

## Consequences

- A stranger with a Proxmox host gets a working, reachable Proxcloud from one
  pasted line, with no repo clone, no hand-built token, and no hand-written
  `.env`. That is the wave's entire justification, and everything above is in
  service of it.
- The private CD path is untouched: no file under `deploy/` and none of the four
  existing workflows change. The two paths will drift over time — when the QA
  compose shape changes, the installer templates must be updated deliberately.
  That drift is accepted as the price of not entangling a public installer with
  a pipeline that holds production credentials.
- The plain-HTTP default means portal traffic on the LAN is unencrypted. This is
  stated plainly in the summary and README, with `tls internal` one advanced-mode
  answer away. We chose a working default over a default that shows a browser
  certificate warning to every first-time user.
- `FRONTEND_ORIGIN` being an IP makes the deployment fragile to a DHCP lease
  change. The state file records the IP, `proxcloud reconfigure` re-renders both
  the `.env` and the printed URL, and the summary tells the user to reserve the
  lease (or use advanced mode's static IP).
- Root-in-container is a real deviation from `first-boot.sh`. It is defensible
  for an appliance container with no inbound push path, and it is written down
  here so the deviation is a decision rather than an oversight.
- `pct exec`-only transport means the installer cannot support a *remote* PVE
  host. That is correct for this wave — the one-liner is documented as "run on
  the Proxmox host" — but it forecloses a "install to a host over SSH" mode
  without a new transport seam.
- Every prompt has a `PC_*` override and `PC_YES=1` exists, so the whole
  installer is exercisable non-interactively in the mocked test suite
  (ADR-0033) — the interactive path is not a testing blind spot.

## Alternatives considered

- **A single monolithic `install.sh` in the repo root** (community-scripts'
  older shape). Rejected: a 1500-line root script is unreviewable in the moment a
  user is deciding whether to pipe it to `bash` as root, and updating it means
  the `main`-branch copy and the released copy drift. The thin-bootstrap split
  (ADR-0033) makes exactly the file the user reads short and stable.
- **A privileged LXC** to make Docker "just work". Rejected: a privileged
  container running Docker is effectively host root, which is not something to
  hand a stranger by default. `nesting=1,keyctl=1` on an unprivileged container
  is the supported, proven configuration.
- **A VM instead of an LXC.** Rejected for v1: slower to provision, requires a
  cloud-init-capable image and more host resources, for no benefit on a homelab
  appliance. Deferred, with the guest type kept as a parameter so it is additive.
- **SSH into the guest** for file/command transport (mirroring
  `deploy/host/*/bootstrap.sh`). Rejected: adds a keypair, an `sshd`, and a
  listening port to gain what root-on-host already has via `pct`. Strictly more
  credential surface for zero capability.
- **Reusing `deploy/host/qa/docker-compose.yml` and `bootstrap.sh` directly**
  (symlink or `git clone` the repo into the guest). Rejected: it couples the
  public installer to files whose purpose is a credentialed, SHA-pinned CD wave,
  and it would make an installer change a change to the prod pipeline's blast
  radius — the hard boundary this wave is built around. We copy the *recipes*
  (Postgres TLS cert, migrator profile, health gate), not the files.
- **HTTPS-by-default with `tls internal`.** Rejected as the default: every first
  run would end at a browser certificate warning, which reads as "the installer
  is broken" and trains users to click through warnings. Offered in advanced
  mode instead.
- **Setting `PROXCLOUD_INSECURE_COOKIES=true` for the plain-HTTP default.**
  Rejected as unnecessary *and* fatal: the config layer rejects it outside dev
  (`config.go:239`), and the Caddy + `TRUST_PROXY_HEADERS` chain already produces
  the correct cookie attributes over HTTP without disabling anything.
- **Deriving the admin password from a host fingerprint** so it is recoverable.
  Rejected: a recoverable admin password is a guessable admin password. Generate,
  reveal once, reset by reconfigure.
- **An in-guest uninstaller.** Rejected on capability grounds: the container
  cannot `pct destroy` itself and has no `pveum`. Uninstall is host-side by
  necessity, which is also where the typed-name guard belongs.

See ADR-0032 (the Proxmox user/role/token this installer creates and verifies),
ADR-0033 (bootstrap trust chain, version locking, and release plumbing),
ADR-0003 (why the console needs a password credential alongside the token, which
is why the installer generates one), ADR-0008 (project pools, the reason
`Pool.Allocate` is in the granted set), and ADR-0014 (the private CD topology
this path deliberately parallels without touching).
