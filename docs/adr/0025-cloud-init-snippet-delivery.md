# ADR-0025: Cloud-init-as-code delivery via SSH snippet + `cicustom`

Date: 2026-08-28 · Status: accepted · Provisioning/Service catalog

## Context

The service-catalog wave (`feat/service-catalog`) provisions opinionated guests
— a service is more than a bare VM: it needs packages, files, and first-boot
commands (install Docker, drop a compose file, start a unit). Today the deploy
engine can only express the small, fixed cloud-init surface that
`BuildCreateParams` inlines on the qemu create call
(`internal/deploy/params.go:222-252`): `ciuser`, `cipassword`, `sshkeys`,
`nameserver`, `searchdomain`, `ipconfig0`, plus the cloud-init drive on
`ide0=<storage>:cloudinit`. There is no inline PVE parameter for `packages:`,
`write_files:`, or `runcmd:` — those live only in a full cloud-init **user-data**
document.

Proxmox stores such a document as a **snippet** and references it from a guest via
`cicustom = "user=<datastore>:snippets/<file>.yaml"`. The problem: **PVE has no
REST endpoint to upload a snippet.** The `snippets` storage content type is
writable only over the node filesystem; the vendored `luthermonson/go-proxmox`
client exposes no snippet-upload call and rejects the `snippets` content type, and
the `proxmox.Client` interface (`internal/proxmox/client.go`) has neither a
snippet-write nor a guest-exec method — by design, it is a pure `/api2/json` seam.

Prod already solved exactly this for its own first-boot cloud-init: the Terraform
`bpg/proxmox` provider writes `content_type = "snippets"` to
`var.snippet_datastore` **over SSH/SFTP** (the provider `ssh` block,
`deploy/terraform/main.tf:18-26,81-94`, rendering
`templates/cloud-init.yaml.tftpl`). The catalog needs the same capability, at
request time, from the backend.

## Decision

**The backend renders each catalog service's cloud-init user-data server-side and
delivers it as a snippet written over SSH/SFTP to the node's snippet datastore,
then references it from the guest with `cicustom`.** Concretely, per deployment:

1. Render the service's `cloud-init.yaml.tftpl` (ADR-0026) with the request's
   inputs (credentials, packages, ports) into a complete `#cloud-config`
   user-data document.
2. Open an SSH/SFTP session to the target node and write the document to the
   snippet datastore as `snippets/proxcloud-<vmid>-<service>.yaml`, using a new
   `github.com/pkg/sftp` dependency over `golang.org/x/crypto/ssh`. This is a new
   `proxmox.Client` capability (a `PutSnippet(node, datastore, name, data)`-shaped
   method for the backend-engineer to add); it is the **only** method that touches
   the node outside the API token.
3. Set `cicustom = "user=<datastore>:snippets/<file>.yaml"` on the create call
   and keep `ide0=<storage>:cloudinit` so PVE still generates the network/meta
   drive.

### The critical override contract (must be encoded in the renderer)

When `cicustom user=` is set, **PVE stops emitting the inline
`ciuser`/`cipassword`/`sshkeys` into the cloud-init user-data** — the custom file
*replaces* the user-data section wholesale. Therefore the rendered snippet MUST
itself carry the account setup: `users:` with `ssh_authorized_keys:`, and
`chpasswd:`/`passwd` for any password credential. The inline `ciuser` etc. from
`params.go:222-234` are **not** sent on a catalog create; sending both is a silent
footgun (the operator sets a password that never lands). What survives is the
**meta-data/network** side: `ipconfig0`, `nameserver`, and `searchdomain` are
carried by the PVE-generated `ide0` cloudinit drive and are **not** overridden by
`user=`, so those inline params stay. The renderer owns credentials; PVE owns
network config.

### v1 scope: qemu only

Seeded services are VMs (qemu). LXC has no cloud-init datasource — its
provisioning path (`params.go:191-209`) takes `password`/`ssh-public-keys`
directly and cannot consume a `cicustom` snippet — so catalog services declare
`kind: qemu` (ADR-0026) and the snippet path is qemu-only in v1.

### New trust surface and its mitigations

This gives the backend **SSH write access to the Proxmox node**, a strictly larger
privilege than the API token it has held until now. Because that is a real
escalation of blast radius, it ships behind guardrails:

- **Host-key verification is mandatory.** The SSH client pins the node's host key
  from configured known-hosts; `ssh.InsecureIgnoreHostKey` is **forbidden** — no
  homelab-convenience escape hatch (unlike `PROXMOX_TLS_INSECURE`, which only
  affects the API TLS chain, not a shell channel).
- **Least-privilege SSH user.** A dedicated, non-root account whose only job is
  writing snippets; not the Terraform provision user, not `root@pam`.
- **Path confinement.** Writes are constrained to `<snippet_datastore>/snippets/`
  with a `proxcloud-`-prefixed, `[a-z0-9-]`-validated filename; the renderer never
  interpolates request strings into the path.
- **New dependency:** `github.com/pkg/sftp` (plus `golang.org/x/crypto/ssh`),
  proven and narrow.
- **Feature gate:** `CATALOG_ENABLED`, **off by default**, following the existing
  default-off `envBool` gates (`config.go:188-190`, `SCHEDULER_ENABLED` /
  `TTL_ENABLED`). With SSH config absent or the gate off, the catalog and its SSH
  seam do not load, so the classic API-token-only deployment is unchanged.

## Consequences

- The catalog can express arbitrary first-boot setup (packages, files, units),
  which bare inline cloud-init cannot — this is what makes a "service" more than a
  VM.
- The backend now holds two credentials against Proxmox (API token + snippet SSH
  key) and a new class of failure (SSH unreachable / host-key mismatch /
  datastore full) that the deploy engine must surface honestly, not swallow. A
  snippet-write failure fails the deployment before the create call, with the real
  error.
- We converge on prod's existing delivery mechanism (`main.tf:81-94`), so there is
  one well-understood way snippets reach the node, not two.
- The override contract is a permanent correctness constraint: any future edit to
  the inline cloud-init params in `params.go` must not assume they reach a
  catalog guest. This is documented at the renderer and enforced by keeping the
  catalog create path from ever emitting `ciuser`/`cipassword`/`sshkeys`.
- With `CATALOG_ENABLED` off (the default), Proxcloud remains API-token-only; the
  SSH trust surface exists solely for installations that opt in.

## Alternatives considered

- **Pre-staged snippets only** (ship a fixed library of snippets on the node, no
  per-request write). Rejected: a static snippet cannot carry the user's chosen
  username/password/SSH key, so it breaks per-request credential injection — the
  whole point of a self-service catalog. It also drifts from the repo definition
  (ADR-0026) the moment a service changes.
- **A separate snippet-writer sidecar** with its own SSH creds, called by the
  backend over RPC. Rejected: adds a process, a second deployment unit, and an
  internal API to secure, to move one file — disproportionate for a homelab
  product built by one engineer. The SSH seam belongs behind the `proxmox.Client`
  interface like every other Proxmox capability.
- **Inline cloud-init params only** (extend `BuildCreateParams`). Rejected on
  capability grounds: PVE exposes no inline parameter for `packages:`/`runcmd:`/
  `write_files:`; there is no way to express service first-boot without a
  user-data document, and no way to deliver that document without the snippet
  path.
- **Guest-agent `exec` to run setup after boot** instead of cloud-init. Rejected:
  `proxmox.Client` has no exec method (client.go), it requires the agent already
  running (a post-boot race), and it replaces declarative first-boot with
  imperative remote commands — strictly worse than cloud-init-as-code.

See ADR-0026 (definition format the renderer consumes) and ADR-0028 (how a
snippet-provisioned guest is detected as ready).

## Addendum (2026-08-30): degrade, don't crash on a misconfigured writer

Originally, with `CATALOG_ENABLED=true`, a snippet-writer that could not be built
(missing SSH vars, an unreadable key, a bad `known_hosts`) was a fatal boot error:
`config.Load` validated the six SSH vars as required, and `main.go` called
`os.Exit(1)` if `NewSnippetWriter` failed. In practice this crash-looped the
**entire** control plane (auth, resources, everything) for a misconfiguration of an
optional, feature-flagged capability — a QA `.env` pointing `PROXMOX_NODE_SSH_KEY_PATH`
at a host path absent inside the container took the whole backend down.

The boot contract is now **degrade, not crash**:

- `config.Load` no longer fatally validates the catalog SSH vars.
- `main.go` builds the writer best-effort (`buildSnippetWriter`); on failure it logs
  loudly and keeps serving, leaving `engine.Snippets` unset and catalog provisioning
  **not-ready**.
- `ProvisionService` and `CreateSet` short-circuit to **503 `unavailable`** before any
  reserve/quota/deploy work, so a misconfig never leaks a reservation, half-provisions
  a set, or reaches Proxmox. Read-only catalog list/get keep serving.
- `catalog.Load()` stays fail-fast — a malformed embedded definition is a build bug,
  not an ops misconfig.

The trust surface is unchanged: host-key verification stays **mandatory** inside
`NewSnippetWriter` (no `InsecureIgnoreHostKey`, no insecure fallback). Only the
failure *mode* changed — from process exit to a scoped, honest 503.
