# ADR-0027: Credential-injection safety, no stored service credentials, and Vault honesty

Date: 2026-08-28 · Status: accepted · Provisioning/Service catalog · Security-critical

## Context

A catalog service is provisioned by rendering a `#cloud-config` snippet
server-side (ADR-0025) and delivering it over SSH. That snippet carries
credential-shaped values: a service superuser password and the operator's SSH
public keys, and — from Phase C — a user-supplied username/password. These
strings are attacker-influenced. They may legally contain `" ' $ \` \ newline
# : | > -` and `$(...)`, and the snippet is both a **YAML document** and a source
of **`runcmd` shell text**. Concatenating a raw value into either is a
root-code-execution bug at first boot in two distinct ways (docs/proxmox/
cloud-init.md §4):

1. **YAML-structure breakage / injection** — a `:`, `#`, quote, block-scalar
   marker, or an embedded newline can terminate a scalar and inject a new
   top-level key (a second `runcmd`, an extra `users:` entry).
2. **Shell injection in `runcmd`** — `$(…)`, backticks, `;`, `|`, `&&` in a value
   that reaches a shell run as root at first boot.

A code-review finding for this wave: the SSH **public keys** are exactly as
dangerous as the password — a crafted key with an embedded newline can open a new
top-level YAML key. Keys must go through the same pipeline as the password, not
be interpolated raw into `ssh_authorized_keys:`.

Two further questions this wave must settle authoritatively: **where generated
secrets live** (they must not accumulate a server-side password store), and how
to model services (HashiCorp Vault) whose security model is **not** user/pass and
whose secrets Proxcloud must never possess.

The mechanism below is **already implemented in Phase A** (`internal/catalog/
render.go`, `services/postgresql/cloud-init.yaml.tftpl`, `internal/handlers/
service_catalog.go`); this ADR records it as the standing contract and extends it
to Phase C (user-supplied credentials) and Phase D (Vault).

## Decision

### 1. Injection safety via mandatory base64 transport

**Every untrusted credential-shaped value — generated OR user-supplied passwords,
usernames, AND SSH public keys — is base64-encoded at render time and decoded
in-guest inside a `runcmd`. The raw value never touches YAML structure or a shell
parser.** The base64 alphabet (`[A-Za-z0-9+/=]`) contains no YAML and no shell
metacharacters, so the interpolated token cannot break the document or the command
line regardless of the underlying bytes. In-guest the decoded value exists only as
a double-quoted shell variable / doubled-single-quote SQL literal and is never
re-split (docs/proxmox/cloud-init.md §4.3).

- The renderer is a **pure transport of already-encoded blobs**: `CloudInitInput`
  carries `SuperuserUserB64`, `SuperuserPassB64`, and `SSHKeysB64 []string`; the
  handler encodes with `catalog.B64` / `catalog.B64Each` before calling
  `RenderCloudInit`. The template performs no encoding and never receives a raw
  secret.
- The template decodes each blob with `printf %s '<b64>' | base64 -d` and
  installs the password via a SQL literal (single quotes doubled) and the keys one
  per line into `authorized_keys` — never via YAML `ssh_authorized_keys:` or
  `chpasswd:` structure.
- **All catalog input is treated as hostile; validation is server-authoritative.**
  `deploy.Validate` runs on the assembled request *before* the snippet is rendered,
  so the guest name (which becomes the hostname) is rejected before interpolation.
- **The enforcement is a permanent CI regression test, not a review checklist.**
  The hostile-input golden tests (`render_test.go`) fuzz `" ' $ \` \n # : |` and
  `$(reboot)` / a newline-injecting fake SSH key, then assert the rendered snippet
  (a) parses as valid YAML, (b) has exactly the expected top-level keys, (c) never
  contains the raw fragment, and (d) round-trips the base64 back to the exact
  secret. Any future template edit that reintroduces raw interpolation fails CI.
- The same rule extends to **any** value reaching PVE params, not just the
  snippet: `sshkeys` on the qemu config is declared `urlencoded` in the schema and
  is URL-escaped accordingly (docs §4.3).

This structurally defeats both YAML-structure breakage and shell/`runcmd`
injection. **Phase C inherits it unchanged** — a user-supplied password/username
is just another raw value fed through `B64`; no new sink is introduced.

### 2. No stored service credentials

**A generated secret is surfaced exactly once and never persisted.** It is minted
with `crypto/rand` (`generatePassword`, 18 random bytes → a 24-char URL-safe
string), returned only in the `202` `ProvisionServiceResponse.GeneratedPassword`
(the one-time reveal UI), and then dropped. It is **never** written to the DB,
emitted to a log line, or placed in the audit `detail`.

- The audit trail records only a **boolean**: `authz.Annotate(ctx,
  "user_credentials", "false")` — whether the credential was user-supplied — never
  the value. The mutation is still audited (tenancy rule 3) without the secret
  entering the audit row.
- The wire types are structurally secret-free downstream of the response:
  `CatalogProvision`, `Deployment`, and `NextStepsInput` carry a **non-secret
  `CredentialHint`** ("user X — password shown once at creation") and never a
  password field. `NextStepsInput` having no password field makes "no secret in
  next-steps" structural, not a review check.
- **Phase C:** a user-supplied credential is used *only* to render the snippet for
  the provisioning task and is not retained past it — same lifetime as a generated
  one, no server-side store either way. The one difference is the audit boolean
  flips to `"true"`.

### 3. Password policy (Phase C)

User-supplied passwords are validated **server-side** against a **length-only
policy: ≥ 12 characters** (NIST-style; the maintainer's decision — length over
composition rules). No character-class or complexity requirement is imposed;
because §1 makes every byte safe to transport, the policy is about strength, not
escaping. Generated passwords already clear this bar (a long `crypto/rand`
string). The check runs before rendering, alongside the existing "at least one SSH
key" guard, so a weak password is rejected before any VMID/quota reservation.

### 4. Vault honesty (Phase D reference case)

For a service whose security model is **not** user/pass — HashiCorp Vault CE, whose
real secrets are the **unseal keys and the initial root token**, produced by
`vault operator init` *inside* the guest — **Proxcloud never sees those secrets.**

- The Vault service definition carries an **empty credential schema**
  (`credentials: []`): there is nothing for Proxcloud to generate, inject, or
  reveal. The provision path skips password generation entirely for such a service.
- Cloud-init installs Vault in **server mode** (uninitialized, sealed). It does
  **not** run `vault operator init` — doing so would force Vault's init/unseal
  model into the user/pass shape and would mean Proxcloud briefly held the root
  token and unseal keys, violating decision 2.
- The rendered **next-steps** instructs the operator to run `vault operator init`
  themselves and to record the unseal keys / root token, which only they ever see.
- `CredentialHint` for such a service points at the init step, not a secret.

This is the canonical example of "no stored service credentials": rather than
inventing a credential to store, Proxcloud **models the service's real security
lifecycle honestly** and stays out of the trust path. Any future service whose
secrets are guest-generated follows this pattern (empty credential schema +
next-steps guidance), not the user/pass one.

## Consequences

- A hostile username, password, or SSH key cannot break the snippet YAML or run a
  command as root — the injection class is closed structurally and held closed by
  a CI test, for both the Phase A generated path and the Phase C user-supplied path.
- There is **no service-credential store to breach, leak, or subpoena.** A user who
  loses a generated password re-provisions or resets it in-guest; Proxcloud cannot
  "look it up," and that is the intended, honest property (aligns with the
  secrets-server-side iron rule and ADR-0028's one-time reveal).
- The audit log proves *that* a credential was set and *by whom*, without ever
  containing the secret — satisfying tenancy audit rule 3 without weakening
  decision 2.
- Vault (and future init/unseal services) fit the catalog without a fake
  credential: the empty-schema + next-steps pattern is now the standing shape for
  guest-generated secrets, so the catalog does not distort a service's real
  security model to fit a form field.
- Cost: the one-time reveal is genuinely one-time; the UI must make the user copy
  it then. Length-only password policy is deliberately permissive and relies on §1
  for safety, not on composition rules — recorded here so it is a known choice.

## Alternatives considered

- **Raw interpolation into YAML / `runcmd`** (the naive `{{ .Password }}` /
  `ssh_authorized_keys: [ {{ .Key }} ]`). Rejected: this *is* the vulnerability —
  YAML-structure injection and root shell execution from credential metacharacters
  (docs §4.1–4.2). Base64 transport exists precisely to eliminate it.
- **Storing generated passwords server-side for later retrieval** (a "show me my
  password again" endpoint backed by the DB). Rejected: it manufactures exactly the
  standing secret store the no-stored-secrets rule forbids, expands the breach blast
  radius, and puts a plaintext (or reversibly-encrypted) credential where audit,
  backups, and logs can reach it. One-time reveal is the deliberate trade.
- **Returning the secret on the deployment-status endpoint** (`GET
  .../deployments/{id}` re-emits `GeneratedPassword`). Rejected: it turns every
  status poll into a secret-bearing response (cached, logged, re-authorizable) and
  defeats one-time reveal. Only the `202` provision response ever carries the value;
  `Deployment` carries the non-secret `CredentialHint` only.
- **Forcing Vault into the user/pass shape** (auto-run `vault operator init`, store
  or reveal the root token). Rejected: it would require Proxcloud to possess the
  unseal keys and root token, breaking decision 2, and misrepresents Vault's
  security model. Empty credential schema + honest next-steps keeps Proxcloud out of
  the trust path (decision 4).
- **Hashing/complexity password rules instead of length-only.** Rejected as the
  default: with §1 guaranteeing safe transport of any byte, strength is the only
  concern, and modern guidance favors length over composition. A pre-hashed
  `chpasswd` value (also base64-piped) remains available for services that want the
  plaintext never to transit the snippet (docs §4.3).

See ADR-0025 (snippet render + SSH delivery), ADR-0026 (service definition format,
the credential schema this ADR keeps empty for Vault), ADR-0028 (one-time reveal
and the non-secret `CredentialHint`/`Connection` fields), and docs/proxmox/
cloud-init.md §4 (the escaping threat model this decision implements).
