#!/usr/bin/env bash
# Proxcloud PROD deploy / blue-green cutover (ADR-0014 §4, ADR-0015).
#
# bash (not /bin/sh) for `pipefail`: migrator/health output is tee'd through
# pipes and a swallowed failure there is unacceptable. Every step is idempotent
# and the whole script is safe to re-run after a partial failure.
#
# Invoked ONLY by the forced-command wrapper (deploy-wrapper.sh), which has
# already validated the argument. We re-validate anyway (defense in depth):
#
#   deploy.sh <ref>        # <ref> = 40-hex git SHA or vMAJOR.MINOR.PATCH
#   deploy.sh --rollback   # flip the proxy back to the warm previous color
set -euo pipefail

ROOT=/opt/proxcloud
STATE_DIR="$ROOT/state"
SNAP_DIR="$ROOT/data/snapshots"
UPSTREAM_DIR="$ROOT/caddy/upstream"
REF_RE='^([0-9a-f]{40}|v[0-9]+\.[0-9]+\.[0-9]+)$'

now()  { date -u +%Y-%m-%dT%H:%M:%SZ; }
log()  { printf '%s [deploy] %s\n' "$(now)" "$*"; }

# ── load non-secret config + GHCR pull creds from .env (values never echoed) ──
[ -f "$ROOT/.env" ] || { printf 'FATAL: %s/.env missing\n' "$ROOT" >&2; exit 1; }
set -a
# shellcheck source=/dev/null
. "$ROOT/.env"
set +a
REGISTRY="${REGISTRY:-ghcr.io/timkrebs}"
SNAPSHOT_RETAIN="${SNAPSHOT_RETAIN:-14}"
MIGRATE_TIMEOUT="${MIGRATE_TIMEOUT:-300}"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-120}"
# Prod defaults the smoke fixture OFF: a real user-facing environment does not
# get a test tenant unless Tim explicitly wants prod smoke (set SMOKE_SEED=1 in
# /opt/proxcloud/.env). Prod and staging use SEPARATE smoke creds (ADR-0016 §4).
SMOKE_SEED="${SMOKE_SEED:-0}"
export REGISTRY

notify() { # notify <priority> <message…>
  local prio="$1"; shift
  [ -n "${NTFY_URL:-}" ] || return 0
  curl -fsS -H "Priority: $prio" -H "Title: proxcloud-prod" -d "$*" "$NTFY_URL" >/dev/null 2>&1 || true
}
die() { printf '%s [deploy][FATAL] %s\n' "$(now)" "$*" >&2; notify high "prod deploy FAILED: $*"; exit 1; }

# ── compose helpers: always feed --env-file so interpolation sees /opt/.../.env ─
compose() { # compose <project> <relpath> args…
  local project="$1" file="$2"; shift 2
  docker compose --env-file "$ROOT/.env" -p "$project" -f "$ROOT/$file" "$@"
}
compose_data()  { compose proxcloud-data  data/docker-compose.yml  "$@"; }
compose_caddy() { compose proxcloud-caddy caddy/docker-compose.yml "$@"; }
compose_color() { local c="$1"; shift; compose "proxcloud-$c" "$c/docker-compose.yml" "$@"; }

backend_port() { case "$1" in blue) echo 18080;; green) echo 28080;; *) echo 0;; esac; }
frontend_port(){ case "$1" in blue) echo 13000;; green) echo 23000;; *) echo 0;; esac; }
idle_of()      { case "$1" in blue) echo green;; green) echo blue;; *) echo blue;; esac; }
read_live()    { tr -d '[:space:]' <"$STATE_DIR/live-color" 2>/dev/null || true; }

ensure_infra() {
  docker network inspect proxcloud-edge     >/dev/null 2>&1 || docker network create proxcloud-edge
  docker network inspect proxcloud-data-net >/dev/null 2>&1 || docker network create proxcloud-data-net
  compose_data  up -d
  compose_caddy up -d
}

ghcr_login() {
  [ -n "${GHCR_TOKEN:-}" ] || return 0
  printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "${GHCR_USER:-}" --password-stdin >/dev/null 2>&1 \
    || log "warning: ghcr login failed (packages may be public — continuing)"
}

snapshot_db() {
  mkdir -p "$SNAP_DIR"
  local ts out
  ts="$(date -u +%Y%m%dT%H%M%SZ)"
  out="$SNAP_DIR/pre-deploy-$ts.sql.gz"
  log "pg_dump snapshot -> $out"
  if ! compose_data exec -T postgres pg_dump -U "${POSTGRES_USER:-proxcloud}" -d "${POSTGRES_DB:-proxcloud}" | gzip >"$out"; then
    rm -f "$out"
    die "pg_dump failed — a snapshot before every prod migration is mandatory"
  fi
  # Retain the newest SNAPSHOT_RETAIN, prune older. Filenames are timestamped
  # and controlled, so line-parsing ls is safe here.
  # shellcheck disable=SC2012
  ls -1t "$SNAP_DIR"/pre-deploy-*.sql.gz 2>/dev/null | tail -n +"$((SNAPSHOT_RETAIN + 1))" | xargs -r rm -f
}

wait_health() { # wait_health <backend-port> <expected-ref>
  local port="$1" ref="$2" got field deadline
  # A 40-hex ref is asserted against .commit; a vX.Y.Z ref against .semver
  # (the image's .commit is always the underlying git SHA, not the tag).
  if [[ "$ref" =~ ^[0-9a-f]{40}$ ]]; then field='.commit'; else field='.semver'; fi
  deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
  while :; do
    if curl -fsS "http://127.0.0.1:$port/api/health" >/dev/null 2>&1; then
      got="$(curl -fsS "http://127.0.0.1:$port/api/v1/version" 2>/dev/null | jq -r "$field" 2>/dev/null || true)"
      if [ "$got" = "$ref" ]; then
        log "idle backend healthy on :$port and ${field#.}=$got"
        return 0
      fi
      log "version not yet matching on :$port (want $ref, got ${got:-<none>})"
    fi
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 3
  done
}

wait_frontend() { # wait_frontend <frontend-port>
  local port="$1" code deadline
  deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
  while :; do
    code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$port/" 2>/dev/null || echo 000)"
    if [ "$code" != "000" ] && [ "$code" -lt 500 ] 2>/dev/null; then
      log "idle frontend responding on :$port (HTTP $code)"
      return 0
    fi
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 3
  done
}

migrate_idle() { # migrate_idle <idle-color> <ref> <backend-port>
  local idle="$1" ref="$2" bport="$3"
  if [ "${USE_MIGRATOR_SERVICE:-0}" = "1" ]; then
    # Dedicated one-shot migrator (ADR ideal). REQUIRES the backend `migrate`
    # subcommand (coordinate item — see deploy/README.md). Bounded by timeout so
    # a binary that lacks the subcommand fails loudly instead of hanging.
    log "migrator service @ $ref (expand/contract) — output captured"
    if ! timeout "$MIGRATE_TIMEOUT" \
      docker compose --env-file "$ROOT/.env" -p "proxcloud-$idle" \
        -f "$ROOT/$idle/docker-compose.yml" run --rm migrator 2>&1 | tee "$STATE_DIR/last-migrate.log"; then
      die "migrator service failed/timed out (backend 'migrate' subcommand present? see deploy/README.md)"
    fi
    return 0
  fi
  # Default (works with today's binary): the backend applies its embedded
  # migrations at boot (store.RunMigrations, golang-migrate, idempotent). Bring
  # the idle BACKEND up first; its startup log IS the migrator output and a
  # failed migration exits the backend so the health gate below fails BEFORE any
  # cutover — old color stays live, no rollback needed.
  log "migrate via idle backend boot (embedded golang-migrate, idempotent)"
  compose_color "$idle" up -d backend
  if ! wait_health "$bport" "$ref"; then
    compose_color "$idle" logs --no-color --tail 200 backend | tee "$STATE_DIR/last-migrate.log" || true
    die "idle backend unhealthy or version mismatch (migration failure? see state/last-migrate.log) — old color still live"
  fi
  compose_color "$idle" logs --no-color --tail 100 backend 2>/dev/null \
    | grep -Ei 'migrat|commit|semver' | tee "$STATE_DIR/last-migrate.log" >/dev/null || true
}

# seed_smoke: idempotent smoke fixture (ADR-0016 §4), mirroring migrate_idle's
# migrator-service branch — a one-shot `seed` compose service run with
# `run --rm` on the just-deployed idle color. SMOKE_SEED-gated (default OFF in
# prod). Writes to the SHARED proxcloud-data Postgres, so it must run AFTER
# migrate_idle (schema present) and BEFORE cutover. Reads SMOKE_EMAIL /
# SMOKE_PASSWORD from /opt/proxcloud/.env via env_file; values never echoed.
seed_smoke() { # seed_smoke <idle-color>
  local idle="$1"
  if [ "$SMOKE_SEED" != "1" ]; then
    log "SMOKE_SEED=$SMOKE_SEED — skipping smoke seed (prod default)"
    return 0
  fi
  if [ -z "${SMOKE_EMAIL:-}" ] || [ -z "${SMOKE_PASSWORD:-}" ]; then
    die "SMOKE_SEED=1 but SMOKE_EMAIL/SMOKE_PASSWORD are unset in $ROOT/.env"
  fi
  log "seed-smoke (idempotent) on $idle — output captured to state/last-seed.log"
  if ! timeout "$MIGRATE_TIMEOUT" \
    docker compose --env-file "$ROOT/.env" -p "proxcloud-$idle" \
      -f "$ROOT/$idle/docker-compose.yml" run --rm seed 2>&1 | tee "$STATE_DIR/last-seed.log"; then
    die "seed-smoke failed/timed out (see state/last-seed.log) — old color still live"
  fi
}

reload_caddy() {
  compose_caddy exec -T caddy caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile \
    || die "caddy reload failed"
}

switch_to() { # switch_to <color> — atomic symlink flip + graceful reload
  local color="$1"
  ln -sfn "$color.caddy" "$UPSTREAM_DIR/active.caddy"   # atomic rename(2)
  reload_caddy
  printf '%s\n' "$color" >"$STATE_DIR/live-color"
  printf '%s %s\n' "$color" "$(now)" >"$STATE_DIR/last-cutover"
}

do_deploy() {
  local ref="$1"
  [[ "$ref" =~ $REF_RE ]] || die "invalid ref: $ref"
  export IMAGE_REF="$ref"

  local live idle bport fport
  live="$(read_live)"; idle="$(idle_of "$live")"
  bport="$(backend_port "$idle")"; fport="$(frontend_port "$idle")"
  log "live=${live:-<none>} -> deploying idle=$idle @ $ref"

  ensure_infra
  ghcr_login
  snapshot_db

  log "pull $idle images @ $ref"
  compose_color "$idle" pull

  migrate_idle "$idle" "$ref" "$bport"          # brings up backend + health-gates

  seed_smoke "$idle"                            # SMOKE_SEED-gated; no-op unless =1

  log "start $idle frontend"
  compose_color "$idle" up -d frontend
  wait_frontend "$fport" || die "idle frontend did not come up — old color still live"

  log "atomic cutover: active.caddy -> $idle.caddy + graceful reload"
  switch_to "$idle"

  log "cutover complete: $idle now live @ $ref (previous color kept warm)"
  notify default "prod live: $idle @ $ref (was ${live:-none})"
}

do_rollback() {
  local live target bport
  live="$(read_live)"
  [ -n "$live" ] || die "no live color recorded — nothing to roll back"
  target="$(idle_of "$live")"
  bport="$(backend_port "$target")"
  log "rollback: current live=$live -> warm=$target; verifying warm color on :$bport"
  curl -fsS "http://127.0.0.1:$bport/api/health" >/dev/null 2>&1 \
    || die "warm color '$target' is not healthy — refusing to roll back to a dead color"
  switch_to "$target"
  log "rollback complete: $target now live (was $live)"
  notify high "prod ROLLBACK: now live=$target (was $live)"
}

main() {
  case "${1:-}" in
    --rollback) do_rollback ;;
    "")         die "usage: deploy.sh <ref> | --rollback" ;;
    *)          do_deploy "$1" ;;
  esac
}
main "$@"
