#!/usr/bin/env bash
# Proxcloud installer bootstrap (ADR-0033 §1).
#
# This file is deliberately thin and VERSION-AGNOSTIC: it resolves a release,
# downloads the installer payload tarball + SHA256SUMS, verifies the checksum
# BEFORE extracting or executing anything, then execs the real orchestrator
# (install/install.sh) from inside the verified payload. It carries no
# per-release data, so the copy on `main` never drifts from any release.
#
#   bash -c "$(curl -fsSL https://raw.githubusercontent.com/timkrebs/proxcloud/main/install.sh)"
#
# Env knobs:
#   PC_VERSION=vX.Y.Z   pin a release (default: latest)
#   PC_SOURCE=local     dev escape hatch — run install/install.sh from this
#                       checkout, skipping download AND verification (loud warning)
set -euo pipefail

REPO="timkrebs/proxcloud"

die() { printf 'proxcloud-install: FATAL: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "must run as root on the Proxmox VE host (try: sudo bash install.sh)"

# ── PC_SOURCE=local: run straight from a checkout (dev/branch testing only) ──
if [ "${PC_SOURCE:-}" = "local" ]; then
  script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
  [ -f "$script_dir/install/install.sh" ] \
    || die "PC_SOURCE=local but $script_dir/install/install.sh not found (run from a repo checkout)"
  printf '\n'
  printf '############################################################\n'
  printf '## WARNING: PC_SOURCE=local — running a NON-RELEASE build ##\n'
  printf '## straight from a checkout. Download and checksum        ##\n'
  printf '## verification are SKIPPED. Never use this path for a    ##\n'
  printf '## real installation.                                     ##\n'
  printf '############################################################\n'
  printf '\n'
  exec bash "$script_dir/install/install.sh" "$@"
fi

# ── Dependencies (everything the bootstrap itself needs) ─────────────────────
for dep in curl sha256sum tar mktemp; do
  command -v "$dep" >/dev/null 2>&1 || die "missing dependency: $dep (apt-get install -y ${dep/sha256sum/coreutils})"
done

# ── Resolve the release ──────────────────────────────────────────────────────
if [ -n "${PC_VERSION:-}" ]; then
  [[ "${PC_VERSION}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
    || die "PC_VERSION must look like vX.Y.Z (got: ${PC_VERSION})"
  tag="$PC_VERSION"
  base="https://github.com/$REPO/releases/download/$tag"
  tarball="proxcloud-installer-$tag.tar.gz"
else
  # `releases/latest/download/<asset>` redirects to the newest release, but we
  # still need the tag to name the tarball asset; resolve it from the redirect.
  latest_url="$(curl -fsSLI -o /dev/null -w '%{url_effective}' \
    "https://github.com/$REPO/releases/latest")" \
    || die "could not reach github.com to resolve the latest release"
  tag="${latest_url##*/}"
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] \
    || die "could not resolve a release tag from $latest_url (no releases yet?)"
  base="https://github.com/$REPO/releases/download/$tag"
  tarball="proxcloud-installer-$tag.tar.gz"
fi

# ── Download into a private staging dir, removed on every exit path ──────────
staging="$(mktemp -d)"
chmod 0700 "$staging"
trap 'rm -rf "$staging"' EXIT

printf 'proxcloud-install: release %s\n' "$tag"
printf 'proxcloud-install: downloading %s\n' "$base/$tarball"
if ! curl -fsSL -o "$staging/$tarball" "$base/$tarball"; then
  die "download failed: $base/$tarball
  This release may not have installer assets (they attach shortly after a
  release is published — retry in a few minutes), or the tag has no release.
  Pin a known-good version with: PC_VERSION=vX.Y.Z bash install.sh"
fi
curl -fsSL -o "$staging/SHA256SUMS" "$base/SHA256SUMS" \
  || die "download failed: $base/SHA256SUMS (release is missing its checksum file)"

# ── Verify BEFORE extracting or executing anything (the load-bearing step) ───
(
  cd "$staging"
  awk -v f="$tarball" '$2 == f' SHA256SUMS > "SHA256SUMS.tarball"
  [ -s "SHA256SUMS.tarball" ] || die "SHA256SUMS has no entry for $tarball"
  if ! sha256sum -c "SHA256SUMS.tarball"; then
    printf 'expected: %s\n' "$(cut -d' ' -f1 "SHA256SUMS.tarball")" >&2
    printf 'actual:   %s\n' "$(sha256sum "$tarball" | cut -d' ' -f1)" >&2
    die "checksum MISMATCH for $tarball — refusing to run it. The download may
  be corrupt or tampered with. Do not retry blindly; verify by hand first."
  fi
)
printf 'proxcloud-install: checksum verified\n'

tar -xzf "$staging/$tarball" -C "$staging" || die "could not extract $tarball"
[ -f "$staging/install/install.sh" ] \
  || die "payload is malformed: install/install.sh missing from $tarball"

# Hand cleanup duty to the orchestrator: exec replaces this process, so our
# EXIT trap will not fire. The orchestrator removes PC_STAGING_DIR on ITS exit.
trap - EXIT
export PC_STAGING_DIR="$staging"
exec bash "$staging/install/install.sh" "$@"
