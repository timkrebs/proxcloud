#!/usr/bin/env bash
# Proxcloud QA deploy (ADR-0022, mirrors ADR-0014 §4.1). Single stack, no
# blue/green, no rollback (QA is disposable — rebuild from scratch instead).
# Idempotent.
#
# Invoked ONLY by the forced-command wrapper (already validates the arg):
#   deploy.sh <ref>        # 40-hex git SHA or vMAJOR.MINOR.PATCH
#   deploy.sh --rollback   # rejected: qa has no rollback
set -euo pipefail

ROOT=/opt/proxcloud
STATE_DIR="$ROOT/state"
REF_RE='^([0-9a-f]{40}|v[0-9]+\.[0-9]+\.[0-9]+)$'
BACKEND_PORT=8080
FRONTEND_PORT=3000

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { printf '%s [deploy-qa] %s\n' "$(now)" "$*"; }

[ -f "$ROOT/.env" ] || { printf 'FATAL: %s/.env missing\n' "$ROOT" >&2; exit 1; }
set -a
# shellcheck source=/dev/null
. "$ROOT/.env"
set +a
REGISTRY="${REGISTRY:-ghcr.io/timkrebs}"
MIGRATE_TIMEOUT="${MIGRATE_TIMEOUT:-300}"
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-120}"
# QA always wants the smoke fixture so smoke-qa can log in (ADR-0016).
SMOKE_SEED="${SMOKE_SEED:-1}"
export REGISTRY

notify() { local prio="$1"; shift; [ -n "${NTFY_URL:-}" ] || return 0
  curl -fsS -H "Priority: $prio" -H "Title: proxcloud-qa" -d "$*" "$NTFY_URL" >/dev/null 2>&1 || true; }
die() { printf '%s [deploy-qa][FATAL] %s\n' "$(now)" "$*" >&2; notify high "qa deploy FAILED: $*"; exit 1; }

compose() { docker compose --env-file "$ROOT/.env" -p proxcloud-qa -f "$ROOT/docker-compose.yml" "$@"; }

# seed_smoke: idempotent smoke fixture (ADR-0016 §4), mirroring the migrator —
# a one-shot `seed` compose service (entrypoint proxcloud seed-smoke) run with
# `run --rm`. SMOKE_SEED-gated (default ON in qa). Reads SMOKE_EMAIL /
# SMOKE_PASSWORD from /opt/proxcloud/.env via the service's env_file; the values
# are never echoed. Output captured. MUST run AFTER migrations so the schema
# exists, and BEFORE the smoke gate so the smoke user can log in.
seed_smoke() {
  if [ "$SMOKE_SEED" != "1" ]; then
    log "SMOKE_SEED=$SMOKE_SEED — skipping smoke seed"
    return 0
  fi
  if [ -z "${SMOKE_EMAIL:-}" ] || [ -z "${SMOKE_PASSWORD:-}" ]; then
    die "SMOKE_SEED=1 but SMOKE_EMAIL/SMOKE_PASSWORD are unset in $ROOT/.env"
  fi
  log "seed-smoke (idempotent) — output captured to state/last-seed.log"
  if ! timeout "$MIGRATE_TIMEOUT" \
    docker compose --env-file "$ROOT/.env" -p proxcloud-qa -f "$ROOT/docker-compose.yml" \
      run --rm seed 2>&1 | tee "$STATE_DIR/last-seed.log"; then
    die "seed-smoke failed/timed out (see state/last-seed.log)"
  fi
}

ghcr_login() {
  [ -n "${GHCR_TOKEN:-}" ] || return 0
  printf '%s' "$GHCR_TOKEN" | docker login ghcr.io -u "${GHCR_USER:-}" --password-stdin >/dev/null 2>&1 \
    || log "warning: ghcr login failed (packages may be public — continuing)"
}

wait_health() { # wait_health <expected-ref>
  local ref="$1" got field deadline
  # 40-hex ref -> assert .commit; vX.Y.Z ref -> assert .semver.
  if [[ "$ref" =~ ^[0-9a-f]{40}$ ]]; then field='.commit'; else field='.semver'; fi
  deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
  while :; do
    if curl -fsS "http://127.0.0.1:$BACKEND_PORT/api/health" >/dev/null 2>&1; then
      got="$(curl -fsS "http://127.0.0.1:$BACKEND_PORT/api/v1/version" 2>/dev/null | jq -r "$field" 2>/dev/null || true)"
      if [ "$got" = "$ref" ]; then log "backend healthy and ${field#.}=$got"; return 0; fi
      log "version not matching yet (want $ref, got ${got:-<none>})"
    fi
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 3
  done
}

wait_frontend() {
  local code deadline
  deadline=$(( $(date +%s) + HEALTH_TIMEOUT ))
  while :; do
    code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$FRONTEND_PORT/" 2>/dev/null || echo 000)"
    if [ "$code" != "000" ] && [ "$code" -lt 500 ] 2>/dev/null; then log "frontend responding (HTTP $code)"; return 0; fi
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 3
  done
}

do_deploy() {
  local ref="$1"
  [[ "$ref" =~ $REF_RE ]] || die "invalid ref: $ref"
  export IMAGE_REF="$ref"
  mkdir -p "$STATE_DIR"

  ghcr_login
  log "start postgres"
  compose up -d postgres

  log "pull backend/frontend @ $ref"
  compose pull backend frontend

  if [ "${USE_MIGRATOR_SERVICE:-0}" = "1" ]; then
    log "migrator service @ $ref — output captured"
    if ! timeout "$MIGRATE_TIMEOUT" \
      docker compose --env-file "$ROOT/.env" -p proxcloud-qa -f "$ROOT/docker-compose.yml" \
        run --rm migrator 2>&1 | tee "$STATE_DIR/last-migrate.log"; then
      die "migrator service failed/timed out (backend 'migrate' subcommand present? see deploy/README.md)"
    fi
  fi

  log "start backend (applies embedded migrations at boot)"
  compose up -d backend
  if ! wait_health "$ref"; then
    compose logs --no-color --tail 200 backend | tee "$STATE_DIR/last-migrate.log" || true
    die "backend unhealthy or version mismatch (migration failure? see state/last-migrate.log)"
  fi

  # Schema is present now (backend healthy => migrations applied). Seed the smoke
  # fixture before the smoke gate so smoke-qa's login assertion can pass.
  seed_smoke

  log "start frontend + caddy"
  compose up -d frontend caddy
  wait_frontend || die "frontend did not come up"

  log "qa deploy complete @ $ref"
  notify default "qa deployed @ $ref"
}

main() {
  case "${1:-}" in
    --rollback) die "qa has no rollback (disposable) — rebuild from scratch (see docs/runbooks/qa-rebuild.md)" ;;
    "")         die "usage: deploy.sh <ref>" ;;
    *)          do_deploy "$1" ;;
  esac
}
main "$@"
