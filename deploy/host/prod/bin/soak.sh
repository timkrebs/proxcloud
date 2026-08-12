#!/usr/bin/env bash
# Proxcloud PROD soak / cleanup sweep (ADR-0014 §5). Invoked ONLY by the
# forced-command soak-wrapper.sh (the `soak` verb, no arguments).
#
# READ-ONLY w.r.t. the cutover: it NEVER flips active.caddy and NEVER rewrites
# state/live-color. It does exactly two things, both idempotent and safe to run
# hourly:
#   1. stop the ALREADY-retired color once it is past its soak window (SOAK_HOURS,
#      default 24h) — never the live color;
#   2. prune old LOCAL images, keeping the newest SOAK_KEEP_IMAGES per repo.
#
# Fails SAFE: any ambiguity about which color is live (marker disagrees with
# live-color, or Caddy is not pointing where we think) => it stops NOTHING and
# only prunes. A wave sharing concurrency group deploy-pve01 (ADR-0014 §3) means
# a soak never races a live cutover. No secret is ever echoed.
set -euo pipefail

ROOT=/opt/proxcloud
STATE_DIR="$ROOT/state"
UPSTREAM_DIR="$ROOT/caddy/upstream"

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { printf '%s [soak] %s\n' "$(now)" "$*" | tee -a "$STATE_DIR/soak.log" 2>/dev/null || printf '%s [soak] %s\n' "$(now)" "$*"; }

[ -f "$ROOT/.env" ] || { printf 'FATAL: %s/.env missing\n' "$ROOT" >&2; exit 1; }
set -a
# shellcheck source=/dev/null
. "$ROOT/.env"
set +a
REGISTRY="${REGISTRY:-ghcr.io/timkrebs9}"
SOAK_HOURS="${SOAK_HOURS:-24}"
SOAK_KEEP_IMAGES="${SOAK_KEEP_IMAGES:-10}"
export REGISTRY

compose_color() { local c="$1"; shift; docker compose --env-file "$ROOT/.env" -p "proxcloud-$c" -f "$ROOT/$c/docker-compose.yml" "$@"; }
idle_of()       { case "$1" in blue) echo green;; green) echo blue;; *) echo "";; esac; }

# ── stop the retired color once it is past its soak window ────────────────────
stop_retired() {
  local live retired lc_color lc_ts lc_epoch age_s threshold_s active_target

  if [ ! -f "$STATE_DIR/last-cutover" ]; then
    log "no state/last-cutover yet — nothing cut over; skipping color stop"
    return 0
  fi
  read -r lc_color lc_ts <"$STATE_DIR/last-cutover" || true
  live="$(tr -d '[:space:]' <"$STATE_DIR/live-color" 2>/dev/null || true)"

  # Fail safe: the cutover marker's color must agree with live-color.
  if [ -n "$live" ] && [ -n "$lc_color" ] && [ "$live" != "$lc_color" ]; then
    log "WARN live-color=$live disagrees with last-cutover color=$lc_color — skipping color stop (fail safe)"
    return 0
  fi
  [ -n "$live" ] || live="$lc_color"
  case "$live" in blue|green) ;; *) log "live color unknown ('$live') — skipping color stop"; return 0;; esac
  retired="$(idle_of "$live")"

  # Extra safety: Caddy must actually point at the live color right now, else a
  # cutover may be in flight — do not touch any color.
  active_target="$(basename "$(readlink "$UPSTREAM_DIR/active.caddy" 2>/dev/null || printf '')" 2>/dev/null || true)"
  if [ -n "$active_target" ] && [ "$active_target" != "$live.caddy" ]; then
    log "WARN active.caddy -> ${active_target:-<none>} but live-color=$live — cutover may be in flight; skipping color stop"
    return 0
  fi

  # Age of the retirement = time since the CURRENT color went live (guest is
  # Linux => GNU `date -d`). Unparseable timestamp => fail safe, skip.
  if ! lc_epoch="$(date -d "$lc_ts" +%s 2>/dev/null)"; then
    log "WARN could not parse last-cutover timestamp '$lc_ts' — skipping color stop"
    return 0
  fi
  age_s=$(( $(date +%s) - lc_epoch ))
  threshold_s=$(( SOAK_HOURS * 3600 ))
  if [ "$age_s" -lt "$threshold_s" ]; then
    log "retired color=$retired age=${age_s}s < soak ${SOAK_HOURS}h — keeping it warm"
    return 0
  fi

  if [ -z "$(compose_color "$retired" ps -q 2>/dev/null)" ]; then
    log "retired color=$retired already stopped (age=${age_s}s) — nothing to do"
    return 0
  fi
  log "retired color=$retired past ${SOAK_HOURS}h soak (age=${age_s}s) — stopping it (live=$live untouched)"
  compose_color "$retired" stop
  log "retired color=$retired stopped"
}

# ── prune old local images, keeping the newest N tags per repo ────────────────
# `docker rmi <repo:tag>` (no -f) refuses to remove an image backing a running
# container, so the live + warm colors are protected by construction; that error
# is tolerated. We prune by repo:tag (not image ID) so a SHA/semver double-tag on
# one image is handled correctly.
prune_images() {
  local repo keep
  keep="$SOAK_KEEP_IMAGES"
  for repo in "$REGISTRY/proxcloud-backend" "$REGISTRY/proxcloud-frontend"; do
    docker images --filter "reference=$repo" --format '{{.CreatedAt}}\t{{.Repository}}:{{.Tag}}' \
      | sort -r \
      | awk -v k="$keep" 'NR>k {print $NF}' \
      | while read -r ref; do
          [ -n "$ref" ] || continue
          if docker rmi "$ref" >/dev/null 2>&1; then
            log "pruned image $ref"
          else
            log "kept in-use/undeletable image $ref"
          fi
        done
  done
}

main() {
  mkdir -p "$STATE_DIR"
  log "soak sweep start (SOAK_HOURS=$SOAK_HOURS SOAK_KEEP_IMAGES=$SOAK_KEEP_IMAGES)"
  stop_retired
  prune_images
  log "soak sweep complete"
}
main "$@"
