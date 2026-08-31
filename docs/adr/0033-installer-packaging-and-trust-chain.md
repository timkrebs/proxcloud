# ADR-0033: Installer packaging, versioning & the pipe-to-bash trust chain

Date: 2026-08-31 · Status: accepted · Security-critical · Delivery/Installer

## Context

The installer's headline is a line the user pastes into a **root shell on their
hypervisor**:

```bash
bash -c "$(curl -fsSL https://raw.githubusercontent.com/timkrebs/proxcloud/main/install.sh)"
```

That is, on its face, "download an unknown program from the internet and run it
as root". It is the convention Proxmox users already accept from
community-scripts.org, and refusing to offer it would cost the wave its entire
reason to exist — but it obliges us to be exact about **what** the user is
trusting, **how many** independent things they must trust, and **which** links in
the chain are verified rather than assumed.

Three problems have to be solved together:

1. **Drift.** If the root `install.sh` carries release-specific data (image tags,
   checksums, versions), then the copy on `main` — the one the one-liner always
   fetches — describes whatever was last merged, not the last *release*. Users
   would get a `main`-shaped installer pulling release-shaped images.
2. **Version locking.** An installer and the container images it pulls must move
   as a unit. An installer that pulls `latest` will, sooner or later, deploy an
   image whose schema its migrator step does not match.
3. **Not touching the private pipeline.** ADR-0014 §8 makes the four `ci.yml`
   jobs the *required status checks* on `main`, and pins `deploy.yml` to a single
   serial concurrency group `deploy-pve01`. Adding installer packaging into
   either file would change the required-check set or contend for the deploy
   lock. Neither is acceptable (ADR-0031's hard boundary).

## Decision

### 1. Trust chain: a thin, auditable bootstrap that verifies before it executes

Repo-root **`install.sh` is ~120 lines and version-agnostic** — short enough to
read in one screen, which is the only realistic audit a user performs at the
moment they paste the line. It does exactly this and nothing else:

1. Resolve the release: `PC_VERSION` if pinned, otherwise
   `releases/latest/download/`.
2. Download **`proxcloud-installer-<tag>.tar.gz`** and **`SHA256SUMS`** from that
   GitHub Release.
3. **`sha256sum -c` the tarball — before extracting or executing anything.** A
   mismatch aborts with the expected and actual digests. This is the load-bearing
   step: the bootstrap is the only code that runs unverified, and it is the only
   code short enough to have been read.
4. Extract into a `mktemp -d` at mode **0700**, `exec` `install/install.sh` from
   it, and remove the directory on an `EXIT` trap.

Because the bootstrap contains **no per-release data**, the `main` copy is
identical to every released copy; problem (1) disappears by construction rather
than by discipline. It also means the bootstrap almost never changes, so a
returning user's audit stays valid.

**`PC_SOURCE=local`** is the development/branch escape hatch: it runs
`install/install.sh` straight from a checkout, skipping download and
verification, and prints a loud multi-line warning that it is doing so. It exists
so the installer is developable and testable; it is never part of any documented
user path.

So the user's trust set is, precisely: **GitHub as a distribution host** (that
`raw.githubusercontent.com/…/main/install.sh` and the release assets are what the
repo owner published), and **the ~120 lines they can read**. Everything after
that — the entire multi-file payload doing the actual privileged work — is
verified by a digest fetched over an independent TLS connection.

### 2. Verify-before-run, documented for cautious users

`install/README.md` documents the non-piped path explicitly, because "just read
the script first" is useless advice if the script's own downloads are unchecked:

```bash
curl -fsSLO https://raw.githubusercontent.com/timkrebs/proxcloud/main/install.sh
less install.sh                                    # 120 lines, actually readable
curl -fsSLO .../releases/download/<tag>/proxcloud-installer-<tag>.tar.gz
curl -fsSLO .../releases/download/<tag>/SHA256SUMS
sha256sum -c SHA256SUMS                            # verify by hand
cosign verify-blob --signature … SHA256SUMS        # optional, keyless
bash install.sh
```

**Signing is best-effort keyless `cosign`**, produced at release time, mirroring
the stance `publish.yml` already takes for image signing (`publish.yml:16-17,
191-198`: signing runs `continue-on-error` so a Sigstore outage never fails a
release). Consequently `cosign verify-blob` is documented as an *optional*
strengthening, and `sha256sum -c` — which is always present and never optional —
is the checked link the bootstrap itself depends on. We do not claim a guarantee
whose production step is allowed to fail.

### 3. Version locking: `install/versions.env`, rewritten at release

A single file maps the installer to the artefacts it deploys:

```
INSTALLER_VERSION=<tag>
APP_SEMVER=<vX.Y.Z>
BACKEND_IMAGE=ghcr.io/timkrebs/proxcloud-backend
FRONTEND_IMAGE=ghcr.io/timkrebs/proxcloud-frontend
```

The values committed in the repo are **dev placeholders**. The release workflow
**rewrites `versions.env` inside the tarball** from the release tag, so a
downloaded installer and the images it pulls are always the same version by
construction — nobody has to remember to bump anything, and a stale committed
value cannot ship.

Images are pulled by **exact semver tag**. Never `latest` (ADR-0014 §1 already
bans it for deploys, and an installer pulling a moving tag would silently
mismatch its own migrator step). Not a raw commit SHA either — unlike the private
CD path, where deploy resolves `workflow_run.head_sha` because CD deploys
*commits*, the installer deploys *releases*, and a semver tag is the artefact a
human can recognise, report in a bug, and pin with `PC_VERSION`. Both are
immutable; semver is the one that means something to an installer user.

### 4. Release plumbing: one new workflow, triggered by the Release itself

**New `.github/workflows/installer-release.yml`**, triggered:

- **`on: release: types: [published]`** — the primary path.
- **`workflow_dispatch`** with a tag input — the re-run path. This is required,
  not decorative: `deploy.yml`'s `release` job uses **`gh release edit`** when a
  release already exists (`deploy.yml:583-586`), and `edit` does **not** re-fire
  the `published` event. Without the dispatch fallback, re-running or repairing a
  release would silently produce no installer assets.

Keying on `release: published` is what makes the hard boundary hold **and**
removes a race: `deploy.yml`'s `release` job creates the GitHub Release only
**after prod has succeeded** (it runs on `v*` and sits behind the whole wave,
`deploy.yml:534-586`). By attaching assets post-hoc to an already-created
Release, the installer path needs **zero modification to `deploy.yml`**, cannot
delay or fail a production cutover, and can never publish an installer for a
version that failed to deploy.

Jobs, in order:

1. **`verify-public-pull`** — from a clean hosted runner with **no credentials**,
   pull the backend and frontend images at the release's semver tag. This is an
   **anonymous** pull, which is exactly what an installer user's Proxmox host
   performs. On failure the job **hard-fails** with an explicit message naming the
   package and the "flip the package visibility to Public" fix. A private image
   is the single most likely way this installer breaks for everyone at once, and
   it must not be discovered by a user.
2. **`package`** — rewrite `versions.env` from the tag; run **`shellcheck`** over
   every shipped script (a lint failure must not ship as a root-run script); `tar`
   the `install/` tree; compute **`SHA256SUMS`** covering the tarball *and* the
   standalone `install.sh` and `uninstall.sh` (both are uploaded separately for
   the manual path in §2); best-effort **`cosign sign-blob`**; and
   `gh release upload --clobber` so a dispatch re-run replaces assets rather than
   erroring on a duplicate.

### 5. Installer CI is its own workflow — `ci.yml`'s required checks are untouched

**New `.github/workflows/installer-ci.yml`**, on PRs touching `install/`,
`install.sh`, `uninstall.sh`, or the two new workflow files:

- **`shellcheck`** over every script,
- a **`bats`** suite exercising the installer's pure logic — validators, `render()`
  placeholder refusal, version parsing, the PVE-major privilege branch (ADR-0032
  §1), the idempotent re-run decision table (ADR-0031 §9) — against **mocked**
  `pct`/`pveum`/`pvesm` binaries on `PATH`, with `PC_YES=1` driving the
  non-interactive path,
- **`actionlint` scoped to the two new workflow files only**.

It is a **separate workflow** specifically so that ADR-0014 §8's documented
required-status-check set (`backend`, `frontend`, `contract`, `docker-build`)
does not change. Adding a job to `ci.yml` would silently alter branch protection
semantics; adding a workflow does not. `installer-ci.yml` can be made a required
check later as an explicit, separate decision.

**Neither new workflow joins the `deploy-pve01` concurrency group** (ADR-0014
§3). Packaging is hosted-runner work that touches no homelab infrastructure, and
sharing the serial deploy lock would let a packaging run block or be blocked by a
production cutover for no reason.

### 6. GHCR images are public, and the workflow enforces it

The installer pulls unauthenticated from a stranger's Proxmox host, so
`proxcloud-backend` and `proxcloud-frontend` must be **public packages**. GHCR
package visibility is a repository-owner setting with no first-class Actions API
for flipping it, so: the maintainer flips it **manually, once**, and
`verify-public-pull` (§4) **enforces the invariant on every release**. A manual
one-time step guarded by an automated check beats an automated step that needs a
long-lived elevated token.

## Consequences

- What a user trusts is small, stated, and verified: GitHub as a host, ~120
  readable lines, and a digest check that gates everything else. That is a claim
  we can defend in the README rather than a hand-wave.
- The `main`-branch bootstrap never drifts from releases, because it contains
  nothing release-specific. The corollary is a constraint: **anything
  release-specific added to the root `install.sh` reintroduces the drift bug.**
  Version data belongs in `versions.env`, inside the verified payload.
- An installer and its images are version-locked by construction, so
  "installer 1.4 pulled an image with a newer schema" cannot happen. Users can
  pin with `PC_VERSION` and reproduce a report exactly.
- `deploy.yml`, `ci.yml`, `publish.yml`, and `soak.yml` are unmodified; the
  installer path cannot delay, block, or fail a production deploy, and cannot
  contend for the single self-hosted runner or the `deploy-pve01` lock.
- Because assets attach *after* the Release is created, there is a short window
  where a Release exists with no installer assets. A user hitting
  `releases/latest/download/` in that window gets a 404 from the bootstrap — an
  honest, obvious failure with a retry, not a corrupt install. Acceptable for a
  window measured in the length of one hosted job.
- `cosign` being best-effort means signatures may be absent for some releases.
  Documented as optional; the checksum chain is the guarantee. Promoting signing
  to required would be a change in `publish.yml`'s posture too, and belongs in
  its own ADR.
- GHCR visibility is a manual setting outside version control. The
  `verify-public-pull` gate converts a silent breakage into a red release, but it
  detects rather than prevents — accepted, given the alternative is a long-lived
  admin token in Actions.
- `PC_SOURCE=local` is an unverified execution path that exists in shipped code.
  It is gated behind an explicit env var and prints a loud warning; it is never
  referenced from user documentation.

## Alternatives considered

- **A single self-contained `install.sh` in the repo root** (no payload,
  no verification). Rejected twice over: the file a user is asked to audit
  becomes ~1500 lines (i.e. unaudited in practice), and the `main` copy carries
  release data it cannot keep correct. The split is what makes both the audit and
  the version lock real.
- **Fetching the payload from a raw `main` URL** instead of a release tarball.
  Rejected: there is nothing to verify against (no checksum for a moving branch),
  the payload changes under users mid-merge, and an installer would pull
  unreleased code onto a stranger's hypervisor.
- **`git clone` the repo into the guest** and run from there. Rejected: pulls the
  whole repository (including the private deploy tree) onto a user's host, adds
  `git` as a dependency, and still has no verification story — a clone of `main`
  is not a release.
- **Embedding image tags in the root `install.sh`, updated by a release bot.**
  Rejected: it reintroduces exactly the drift the split removes (the bot's commit
  must land on `main` before the release is usable) and makes the audited file
  churn on every release, so a returning user's audit is never reusable.
- **Packaging inside `publish.yml` or `deploy.yml`.** Rejected: it would put
  installer packaging behind — and potentially inside — the production deploy
  wave, contend for the `deploy-pve01` serial lock, and violate the wave's hard
  boundary. `release: published` gives the same ordering guarantee (prod
  succeeded first) with zero coupling.
- **`on: push: tags: v*`** for the installer release workflow. Rejected: a tag
  push happens *before* the deploy wave runs, so installer assets would be
  published for a version that might never reach prod — the precise inversion of
  the guarantee we want.
- **Adding installer jobs to `ci.yml`.** Rejected: it changes the required-status-
  check set that ADR-0014 §8 documents and branch protection enforces, coupling a
  shell-lint failure to merge eligibility for backend/frontend work.
- **Pulling images by digest (`@sha256:…`).** Rejected: maximally immutable but
  opaque — a user cannot tell which version they are running, cannot pin one
  meaningfully, and every bug report becomes a digest lookup. Semver tags on GHCR
  are immutable in our publishing model (ADR-0014 §1: SHA + semver, never
  `latest`) and are legible.
- **Requiring `cosign` verification in the bootstrap.** Rejected: it would make
  installation depend on a tool most Proxmox hosts do not have and on Sigstore
  being up, for a signature whose *production* step is `continue-on-error`. A
  hard dependency on an optional artefact is a broken install waiting to happen.

See ADR-0031 (the installer this packages, and the payload layout the tarball
contains), ADR-0032 (the privileged operations that only run after the digest
check in §1 passes), and ADR-0014 (the private CD topology — required checks,
concurrency group, and the `release` job this workflow hangs off without
modifying).
