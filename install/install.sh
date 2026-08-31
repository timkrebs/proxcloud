#!/usr/bin/env bash
# Proxcloud installer — orchestrator (ADR-0031). Executed by the repo-root
# bootstrap (install.sh) from a checksum-verified payload, or directly from a
# checkout via PC_SOURCE=local. Runs as root on the Proxmox VE host.
#
# Flow: banner → preflight → existing-install check → mode prompts →
#       token → guest → stack → summary → state file.
set -euo pipefail

# shellcheck source-path=SCRIPTDIR
PC_PAYLOAD_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export PC_PAYLOAD_DIR

# SC1091: every lib below is linted as its own file (installer-ci runs the
# linter over the whole payload) — nothing is gained by following here.
# shellcheck disable=SC1091
{
  source "$PC_PAYLOAD_DIR/lib/core.func"
  source "$PC_PAYLOAD_DIR/lib/preflight.func"
  source "$PC_PAYLOAD_DIR/lib/token.func"
  source "$PC_PAYLOAD_DIR/lib/guest.func"
  source "$PC_PAYLOAD_DIR/lib/stack.func"
  source "$PC_PAYLOAD_DIR/lib/summary.func"
  # Version lock (ADR-0033 §3): dev placeholders in-repo, rewritten at release.
  source "$PC_PAYLOAD_DIR/versions.env"
}
validate INSTALLER_VERSION '^(dev|v[0-9]+\.[0-9]+\.[0-9]+)$' "INSTALLER_VERSION"
validate APP_SEMVER        '^(dev|v[0-9]+\.[0-9]+\.[0-9]+)$' "APP_SEMVER"
validate IMAGE_BACKEND  '^ghcr\.io(/[a-z0-9._-]+){2,}$' "IMAGE_BACKEND"
validate IMAGE_FRONTEND '^ghcr\.io(/[a-z0-9._-]+){2,}$' "IMAGE_FRONTEND"
export INSTALLER_VERSION APP_SEMVER IMAGE_BACKEND IMAGE_FRONTEND

# ── EXIT trap: staging cleanup + failure diagnostics ─────────────────────────
_on_exit() {
  local rc=$?
  # Render staging (host-side secrets in flight) — always removed.
  if [ -n "${PC_RENDER_DIR:-}" ] && [ -d "${PC_RENDER_DIR:-}" ]; then
    rm -rf "$PC_RENDER_DIR"
  fi
  # Payload staging handed over by the bootstrap (unset for PC_SOURCE=local
  # checkouts, which must never be deleted).
  if [ -n "${PC_STAGING_DIR:-}" ] && [ -d "${PC_STAGING_DIR:-}" ]; then
    rm -rf "$PC_STAGING_DIR"
  fi
  if [ "$rc" -ne 0 ]; then
    printf '\n[installer] FAILED (exit %s). Diagnostics:\n' "$rc" >&2
    printf '  - full install log: %s (redacted — safe to share)\n' "${PC_LOG_FILE:-/var/log/proxcloud-install.log}" >&2
    if [ -n "${PC_VMID:-}" ]; then
      printf '  - guest logs: pct exec %s -- bash -c '\''cd /opt/proxcloud && docker compose -p proxcloud logs'\''\n' "$PC_VMID" >&2
    fi
    printf '  - a re-run converges PVE objects; a half-created guest can be removed with: pct destroy <vmid>\n' >&2
  fi
  exit "$rc"
}
trap _on_exit EXIT

banner() {
  printf '\n'
  printf '  ____                      _                 _\n'
  printf ' |  _ \\ _ __ _____  ___ ___| | ___  _   _  __| |\n'
  # shellcheck disable=SC2016  # ASCII art, not an expression
  printf ' | |_) | '\''__/ _ \\ \\/ / __/ _` |/ _ \\| | | |/ _` |\n'
  printf ' |  __/| | | (_) >  < (_| (_| | (_) | |_| | (_| |\n'
  printf ' |_|   |_|  \\___/_/\\_\\___\\___|_|\\___/ \\__,_|\\__,_|\n'
  printf '\n'
  printf ' Proxcloud installer %s (app %s)\n' "$INSTALLER_VERSION" "$APP_SEMVER"
  printf ' Self-service cloud control plane for your Proxmox VE homelab.\n\n'
}

main() {
  core_log_init
  banner
  log "installer $INSTALLER_VERSION starting (app $APP_SEMVER, payload $PC_PAYLOAD_DIR)"

  preflight_run

  # ── Existing-install detection ─────────────────────────────────────────────
  # ╔═══════════════════════════════════════════════════════════════════════╗
  # ║ PHASE-2 STUB (ADR-0031 §9): the Update / Reconfigure / Reinstall /    ║
  # ║ Cancel re-run menu with the adopt-or-abort rule arrives in a later    ║
  # ║ phase. For now an existing install is left strictly untouched.        ║
  # ╚═══════════════════════════════════════════════════════════════════════╝
  if [ -f /etc/proxcloud-installer.conf ]; then
    log "found /etc/proxcloud-installer.conf — Proxcloud is already installed on this host."
    log "Re-run/update support (Update / Reconfigure / Reinstall) arrives in a later installer release."
    log "Nothing was changed. To start over manually: destroy the guest listed in that file and remove it."
    exit 0
  fi

  # ── Mode ───────────────────────────────────────────────────────────────────
  PC_MODE="${PC_MODE:-}"
  menu PC_MODE "Installation mode" "default" "advanced"
  if [ "$PC_MODE" = "advanced" ]; then
    warn "Advanced mode (VMID/CPU/RAM/static IP/TLS/etc.) is coming in a later release —"
    warn "continuing with the Default flow (storage + bridge prompts only)."
  fi

  # Default mode asks ONLY what cannot be defaulted (ADR-0031 §2); everything
  # else is preset here and validated at intake in guest.func.
  PC_HOSTNAME="${PC_HOSTNAME:-proxcloud}"
  PC_CORES="${PC_CORES:-2}"
  PC_MEMORY="${PC_MEMORY:-4096}"
  PC_DISK_GB="${PC_DISK_GB:-32}"

  menu PC_STORAGE_TMPL   "Storage for the container TEMPLATE (content: vztmpl)" "${PC_STORAGE_TMPL_OPTS[@]}"
  menu PC_STORAGE_ROOTFS "Storage for the container ROOT DISK (content: rootdir)" "${PC_STORAGE_ROOTFS_OPTS[@]}"
  menu PC_BRIDGE         "Network bridge for the guest" "${PC_BRIDGE_OPTS[@]}"

  preflight_detect_host_ip "$PC_BRIDGE"

  token_setup
  guest_create
  stack_deploy
  summary_print

  # ── State file (ADR-0031 §9): facts only, NO secrets, mode 0600 ───────────
  local conf=/etc/proxcloud-installer.conf
  {
    printf 'VMID=%s\n'              "$PC_VMID"
    printf 'HOSTNAME=%s\n'          "$PC_HOSTNAME"
    printf 'BRIDGE=%s\n'            "$PC_BRIDGE"
    printf 'STORAGE=%s\n'           "$PC_STORAGE_ROOTFS"
    printf 'GUEST_IP=%s\n'          "$PC_GUEST_IP"
    printf 'PVE_HOST_IP=%s\n'       "$PC_PVE_HOST_IP"
    printf 'INSTALLER_VERSION=%s\n' "$INSTALLER_VERSION"
    printf 'APP_SEMVER=%s\n'        "$APP_SEMVER"
    printf 'TLS_MODE=%s\n'          "http"
    printf 'CREATED_AT=%s\n'        "$(now)"
  } > "$conf"
  chmod 0600 "$conf"
  log "state written to $conf (no secrets)"
  log "install complete — portal: $PC_PORTAL_URL"
}

main "$@"
