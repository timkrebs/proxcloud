# Service catalog — authoring guide

This directory holds the **service catalog**: the curated, versioned set of
one-click provisioning recipes the Proxcloud "Create a resource → from catalog"
gallery offers. Definitions are **code** — they ship embedded in the backend
binary (`//go:embed`, `catalog.go`) and are validated at startup, so a bad
definition fails the process boot, never a half-provisioned guest (ADR-0026).

> **Location note.** ADR-0025/0026 describe the catalog living at the repo root
> (`catalog/`). Go's `//go:embed` cannot embed files outside the module
> (`backend/`) or through `..` path elements, so the embeddable single source of
> truth lives here, under the package that embeds it
> (`backend/internal/catalog/services/`). There is intentionally **one** copy —
> the running image and its catalog can never drift (ADR-0026).

## Layout

One directory per service, named by its stable `id`:

```
services/<id>/
  service.yaml         # metadata + sizing + inputs (schema below)
  cloud-init.yaml.tftpl # the #cloud-config user-data (rendered + uploaded)
  next-steps.md.tftpl  # post-ready guidance (rendered with live host/port)
```

Adding or changing a service is a reviewed, versioned diff — no migration, no
runtime write.

## `service.yaml` schema

| field | type | notes |
|-------|------|-------|
| `id` | string | stable slug `[a-z0-9-]`, **must equal the directory name**, unique |
| `displayName` | string | gallery tile title |
| `description` | string | one-line tile subtitle |
| `icon` | string | design-system icon key (never a hardcoded asset) |
| `category` | string | gallery grouping, e.g. `database` |
| `kind` | `single` | `single` provisions one guest; `set` is reserved (Phase E) and rejected in v1 |
| `guestType` | `qemu` | VM only — cloud-init user-data does not apply to LXC (ADR-0025 §v1) |
| `baseImage.ref` | string | storage-scoped volid of the base image |
| `sizing.default` / `sizing.min` | `{cores,memoryMb,diskGb}` | wizard default and floor; every default ≥ its min |
| `credentials[]` | list | injected credential inputs (see below) |
| `ports` | `[int]` | default service ports (informational + firewall hints) |
| `readiness` | `tcp:<port>` | the completion probe target consumed by the deploy engine (ADR-0028) — the single source of the port, never hardcode it |
| `docs` | url | upstream documentation link |
| `testedOn` | `YYYY-MM-DD` | date this def was last verified on a real PVE |

### `credentials[]`

```yaml
credentials:
  - name: superuser        # logical name of the credential
    username: postgres      # fixed account/role name it applies to
    usernameSettable: false # may the wizard change the username?
    userSettable: true      # may the wizard set the value? (Phase C)
    generatedDefault: true  # backend mints a strong secret when left blank
```

`userSettable` decides whether the credentials wizard step (Phase C) exposes the
field; `generatedDefault` decides whether the backend generates a strong secret
(crypto/rand, length ≥ 12) when the user leaves it blank. In Phase A the value is
always generated and surfaced **once** in the provisioning response.

## `cloud-init.yaml.tftpl` conventions

Rendered with Go `text/template` (`{{ ... }}` — the `.tftpl` extension only
matches prod's naming). The engine writes the result to
`<snippet_datastore>/snippets/proxcloud-<vmid>-<id>.yaml` over SSH/SFTP and
references it with `cicustom "user=..."` (ADR-0025).

**Required elements:**

1. `#cloud-config` first line.
2. `users:` with `ssh_authorized_keys:` (and, if a login password is wanted,
   set it via `chpasswd`/`base64` in `runcmd`). This is mandatory: `cicustom
   user=` makes PVE **drop** the inline `ciuser`/`cipassword`/`sshkeys`, so the
   snippet must carry the login identity (docs/proxmox/cloud-init.md §1.4).
3. `packages:` including **`qemu-guest-agent`** — the deploy engine's
   `configuring` step needs the guest agent to report the VM's IP (ADR-0028).
4. `runcmd:` that `systemctl enable --now qemu-guest-agent` and does the
   service's first-boot setup.

**Network stays inline.** Do NOT put `ipconfig`/DNS in the snippet: PVE still
generates network-config from the create call's `ipconfig0`/`nameserver` because
we only override `user=` (docs §1.5).

### Credential injection (SECURITY-CRITICAL)

Never interpolate a raw credential into YAML or a shell string — a value may
contain `" ' $ \` \n # : |` or `$(...)` and can break the document or inject a
command (docs/proxmox/cloud-init.md §4). Instead:

1. The backend base64-encodes each untrusted value at **render** time and the
   template interpolates only the base64 blob (YAML- and shell-safe alphabet).
2. The template decodes it **in-guest** in `runcmd`, keeping the raw bytes in a
   double-quoted shell variable, and passes it to the service as a properly
   quoted literal (e.g. a psql SQL literal with `''` escaping).

The PostgreSQL definition is the reference: `{{ "{{ .SuperuserPassB64 }}" }}` is
the render-time base64; it is only ever `base64 -d`'d into a shell variable.

## `next-steps.md.tftpl` conventions

Rendered with the live connection details after the guest is `ready`. It MUST
contain the host/port a user connects to and MUST NOT contain any credential
value — the `NextStepsInput` the renderer passes has **no password field**, so a
leak is structurally impossible. Tell the user how they set/retrieve the secret
instead.

## Testing a new service

- `go test ./internal/catalog/...` runs the loader validation and the render
  golden tests. Add a golden case asserting your rendered cloud-init is valid
  YAML and your next-steps contains no credential value.
- Set `CATALOG_ENABLED=true` plus the `PROXMOX_NODE_SSH_*` / `SNIPPET_*` config
  and provision against a real PVE, then update `testedOn`.
