#!/usr/bin/env bash
# One-time move of the PROD proxcloud-edge network onto its pinned addressing
# (ADR-0034). Run by the operator, as root, on the prod guest — see
# docs/runbooks/prod-edge-network-migration.md:
#
#   bin/migrate-edge-network.sh --check   # pre-checks only
#   bin/migrate-edge-network.sh           # pre-checks, then migrate
#
# Every pre-check runs before anything changes. (They create and remove a
# throwaway probe network, and dry-run the live color's migrator, which applies
# nothing the live backend has not already applied.) Once the migration
# starts, the first failing command stops it and the ERR trap prints the state
# the edge is in, with the exact commands to finish or to abort.
#
# The edge is down for the seconds between stopping the old Caddy and starting
# the new one; Postgres and the color containers keep running. The live
# color's backend is then recreated so it applies TRUSTED_PROXY_CIDRS at once —
# only if the migrator dry run shows its image knows the current schema: an
# image older than the schema cannot boot (after a rollback, say). The warm
# color is never recreated here, for the same reason; its next deploy applies
# the setting.
set -Eeuo pipefail

ROOT=/opt/proxcloud
BACKUP=/root/proxcloud-caddy.pre-edge-migration
# Must match bin/ensure-networks.sh, caddy/docker-compose.yml, caddy/Caddyfile.
EDGE_NET=proxcloud-edge
EDGE_SUBNET=10.254.254.0/24
EDGE_IP_RANGE=10.254.254.128/25
EDGE_GATEWAY=10.254.254.1
CADDY_IP=10.254.254.10
TRUSTED=10.254.254.10/32
PROBE_NET=proxcloud-edge-precheck
REF_RE='^([0-9a-f]{40}|v[0-9]+\.[0-9]+\.[0-9]+)$'
HEALTH_TIMEOUT=120

now() { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { printf '%s [edge-migrate] %s\n' "$(now)" "$*"; }
die() { printf '%s [edge-migrate][FATAL] %s\n' "$(now)" "$*" >&2; exit 1; }

compose() { # compose <project> <relpath> args…
  local project="$1" file="$2"; shift 2
  docker compose --env-file "$ROOT/.env" -p "$project" -f "$ROOT/$file" "$@"
}

edge_addressing() {
  docker network inspect "$EDGE_NET" \
    --format '{{range .IPAM.Config}}{{.Subnet}} {{.IPRange}} {{.Gateway}}{{"\n"}}{{end}}'
}

edge_pinned() { edge_addressing | grep -qxF "$EDGE_SUBNET $EDGE_IP_RANGE $EDGE_GATEWAY"; }

# env_value KEY: the value of the last KEY= line in .env, quotes stripped.
# Reads that one key only — nothing else from .env is ever printed.
env_value() {
  local v
  v="$(grep -E "^$1=" "$ROOT/.env" | tail -n 1 | cut -d= -f2-)" || true
  v="${v%\"}"; v="${v#\"}"; v="${v%\'}"; v="${v#\'}"
  printf '%s' "$v"
}

# edge_members: every container attached to the edge network, RUNNING OR
# STOPPED (a stopped one keeps the network in its config and could never start
# again once that network is gone) — except Caddy, which compose recreates.
edge_members() {
  # shellcheck disable=SC2016 # $n is a Go template variable
  docker ps -aq \
    | xargs -r docker inspect --format '{{.Name}}{{range $n, $_ := .NetworkSettings.Networks}} {{$n}}{{end}}' \
    | awk -v net="$EDGE_NET" '{ for (i = 2; i <= NF; i++) if ($i == net) { sub(/^\//, "", $1); print $1 } }' \
    | grep -vx proxcloud-caddy || true
}

backend_port() { case "$1" in blue) echo 18080 ;; green) echo 28080 ;; esac; }
idle_of() { case "$1" in blue) echo green ;; *) echo blue ;; esac; }

wait_http() { # wait_http <url> <seconds>: until the URL answers 2xx
  local deadline=$(($(date +%s) + $2))
  until curl -fsS --max-time 5 -o /dev/null "$1" 2>/dev/null; do
    [ "$(date +%s)" -lt "$deadline" ] || return 1
    sleep 2
  done
}

# ── pre-checks ────────────────────────────────────────────────────────────────
problems=0
live=""
members=()
recreate_live=0
live_ref=""
live_registry=""

ok() { printf '  ok    %s\n' "$*"; }
bad() { printf '  FAIL  %s\n' "$*"; problems=$((problems + 1)); }
note() { printf '  note  %s\n' "$*"; }

precheck() {
  [ "$(id -u)" -eq 0 ] || die "run as root: sudo $0"
  [ -f "$ROOT/.env" ] || die "$ROOT/.env is missing"
  docker network inspect "$EDGE_NET" >/dev/null 2>&1 \
    || die "$EDGE_NET does not exist — nothing to migrate (bin/up-infra.sh creates it pinned)"
  if edge_pinned; then
    log "$EDGE_NET is already on the pinned addressing — nothing to do"
    exit 0
  fi
  log "pre-checks (nothing is changed)"
  trap 'die "pre-checks stopped unexpectedly (line $LINENO) — nothing was changed"' ERR
  local tmp link svc dv major img m
  tmp="$(mktemp)"

  # The live color, the upstream file Caddy will load, and the live containers
  # must agree, or the edge would come back pointing at the wrong color.
  live="$(tr -d '[:space:]' <"$ROOT/state/live-color" 2>/dev/null || true)"
  case "$live" in
    blue | green) ok "live color: $live" ;;
    *) bad "state/live-color is '${live:-<empty>}', not blue or green" ;;
  esac
  link="$(readlink "$ROOT/caddy/upstream/active.caddy" 2>/dev/null || true)"
  if [ -n "$live" ] && [ "$link" = "$live.caddy" ]; then
    ok "caddy/upstream/active.caddy -> $link"
  else
    bad "caddy/upstream/active.caddy -> '${link:-<missing>}' but the live color is '${live:-?}' — re-run bootstrap.sh, which points it at state/live-color"
  fi
  for svc in backend frontend; do
    if [ "$(docker inspect -f '{{.State.Running}}' "proxcloud-$live-$svc" 2>/dev/null || true)" = true ]; then
      ok "proxcloud-$live-$svc is running"
    else
      bad "proxcloud-$live-$svc is not running — the edge would come back to a dead color"
    fi
  done

  # The new files are provisioned; the old ones are saved for an abort.
  if grep -qF "ipv4_address: $CADDY_IP" "$ROOT/caddy/docker-compose.yml" \
    && grep -qF "trusted_proxies static $EDGE_GATEWAY/32" "$ROOT/caddy/Caddyfile"; then
    ok "the new caddy/docker-compose.yml and Caddyfile are in place"
  else
    bad "caddy/ still holds the old files — provision this release first (runbook Step 1)"
  fi
  if [ -f "$BACKUP/docker-compose.yml" ] && [ -f "$BACKUP/Caddyfile" ] \
    && ! grep -qF "ipv4_address: $CADDY_IP" "$BACKUP/docker-compose.yml"; then
    ok "the pre-migration Caddy files are saved in $BACKUP"
  else
    bad "$BACKUP must hold the PRE-migration caddy/ files (runbook Step 1) — the abort path restores them"
  fi
  if [ "$(env_value TRUSTED_PROXY_CIDRS)" = "$TRUSTED" ]; then
    ok ".env sets TRUSTED_PROXY_CIDRS=$TRUSTED"
  else
    bad ".env must set TRUSTED_PROXY_CIDRS=$TRUSTED (runbook Step 2)"
  fi

  # The pinned subnet must be free on this host.
  if edge_addressing | cut -d' ' -f1 | grep -qxF "$EDGE_SUBNET"; then
    note "the old $EDGE_NET already uses $EDGE_SUBNET; replacing the network frees it"
  else
    if ip -4 route show | grep -qF '10.254.254.'; then
      bad "a host route already uses 10.254.254.x: $(ip -4 route show | grep -F '10.254.254.' | head -n 1)"
    else
      ok "no host route uses $EDGE_SUBNET"
    fi
    docker network rm "$PROBE_NET" >/dev/null 2>&1 || true
    if docker network create --subnet "$EDGE_SUBNET" --ip-range "$EDGE_IP_RANGE" \
      --gateway "$EDGE_GATEWAY" "$PROBE_NET" >/dev/null 2>"$tmp"; then
      if docker network rm "$PROBE_NET" >/dev/null 2>&1; then
        ok "docker can create a network on $EDGE_SUBNET"
      else
        bad "the probe network $PROBE_NET was created but could not be removed — remove it by hand"
      fi
    else
      bad "docker cannot create a network on $EDGE_SUBNET: $(tr '\n' ' ' <"$tmp")"
    fi
  fi

  # The new Caddy config must validate against the upstream file it will load.
  if docker run --rm --network none \
    -v "$ROOT/caddy/Caddyfile:/etc/caddy/Caddyfile:ro" \
    -v "$ROOT/caddy/upstream:/etc/caddy/upstream:ro" \
    caddy:2-alpine caddy validate --config /etc/caddy/Caddyfile --adapter caddyfile >"$tmp" 2>&1; then
    ok "the new Caddyfile validates"
  else
    bad "the new Caddyfile does not validate: $(tail -n 3 "$tmp" | tr '\n' ' ')"
  fi

  # Tunnel-only needs a Docker that keeps LAN hosts off loopback-published
  # ports (moby#48721, fixed in 28.0) — or the equivalent raw-table rule.
  dv="$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"
  major="${dv%%.*}"
  case "$major" in
    '' | *[!0-9]*) bad "cannot read the Docker Engine version ('$dv')" ;;
    *)
      if [ "$major" -ge 28 ]; then
        ok "Docker Engine $dv"
      elif iptables -t raw -C PREROUTING ! -i lo -d 127.0.0.0/8 -j DROP 2>/dev/null; then
        ok "Docker Engine $dv, with the raw-table loopback DROP rule"
      else
        bad "Docker Engine $dv lets LAN hosts reach ports published on 127.0.0.1 — upgrade to 28+, or add (and persist): iptables -t raw -I PREROUTING ! -i lo -d 127.0.0.0/8 -j DROP"
      fi
      ;;
  esac

  while IFS= read -r m; do members+=("$m"); done < <(edge_members)
  if [ "${#members[@]}" -gt 0 ]; then
    ok "containers to move: ${members[*]}"
  else
    note "no containers besides Caddy are attached to $EDGE_NET"
  fi

  # Recreate the live backend only if its image can boot against the current
  # schema and .env — which is exactly what its migrator checks.
  if [ "$live" = blue ] || [ "$live" = green ]; then
    img="$(docker inspect -f '{{.Config.Image}}' "proxcloud-$live-backend" 2>/dev/null || true)"
    live_ref="${img##*:}"
    live_registry="${img%/proxcloud-backend:*}"
    if ! [[ "$live_ref" =~ $REF_RE ]] || [ "$live_registry" = "$img" ]; then
      note "the live backend runs '$img', not a release image — it will not be recreated; the next deploy applies TRUSTED_PROXY_CIDRS"
    elif REGISTRY="$live_registry" IMAGE_REF="$live_ref" timeout 180 \
      docker compose --env-file "$ROOT/.env" -p "proxcloud-$live" -f "$ROOT/$live/docker-compose.yml" \
      run --rm --no-deps migrator >"$tmp" 2>&1; then
      recreate_live=1
      ok "migrator dry run on the live image ($live_ref) passed — its backend will be recreated to apply TRUSTED_PROXY_CIDRS"
    else
      note "the live image ($live_ref) cannot boot against the current schema or .env, so its backend will NOT be recreated; the next deploy applies TRUSTED_PROXY_CIDRS, and until then every client shares Caddy's rate-limit bucket. Migrator: $(tail -n 2 "$tmp" | tr '\n' ' ')"
    fi
  fi
  rm -f "$tmp"
  trap - ERR

  [ "$problems" -eq 0 ] || die "$problems pre-check(s) failed — nothing was changed"
  log "all pre-checks passed"
}

# ── migration ─────────────────────────────────────────────────────────────────
STEP=""
detached=()
reattached=()

step() {
  STEP="$*"
  log "== $STEP"
}

# recovery prints the edge's current state and the commands to finish or abort.
recovery() {
  local net pending=() m r caddy
  if ! docker network inspect "$EDGE_NET" >/dev/null 2>&1; then
    net=missing
  elif edge_pinned; then
    net=pinned
  else
    net=old
  fi
  for m in ${detached[@]+"${detached[@]}"}; do
    for r in ${reattached[@]+"${reattached[@]}"}; do
      [ "$m" = "$r" ] && continue 2
    done
    pending+=("$m")
  done
  caddy="$(docker inspect -f '{{.State.Status}}' proxcloud-caddy 2>/dev/null || echo 'removed')"
  local reconnect="  (nothing to re-attach)"
  if [ "${#pending[@]}" -gt 0 ]; then
    reconnect="$(for m in "${pending[@]}"; do printf '  docker network connect %s %s\n' "$EDGE_NET" "$m"; done)"
  fi
  local restore_old="  cp $BACKUP/docker-compose.yml $BACKUP/Caddyfile $ROOT/caddy/
  docker compose --env-file $ROOT/.env -p proxcloud-caddy -f $ROOT/caddy/docker-compose.yml up -d"

  cat <<EOF

State now:
  $EDGE_NET network: $net (old = original addressing, pinned = new, missing = removed)
  detached and not re-attached: ${pending[*]:-(none)}
  Caddy container: $caddy
EOF
  if [ "$caddy" != running ]; then
    echo "  The edge is DOWN until you finish or abort."
  fi
  case "$net" in
    old)
      cat <<EOF
  still attached to the old network: $(docker network inspect "$EDGE_NET" -f '{{range .Containers}}{{.Name}} {{end}}' 2>/dev/null)

To ABORT (back to exactly the pre-migration edge):
$reconnect
$restore_old

To RETRY: run the re-attach commands above, fix the cause, then re-run $0
EOF
      ;;
    missing)
      cat <<EOF

To FINISH (once the cause is fixed):
  bash $ROOT/bin/ensure-networks.sh
$reconnect
  bash $ROOT/bin/up-infra.sh

To ABORT (a Docker-assigned network, as before, and the old Caddy):
  docker network create $EDGE_NET
$reconnect
$restore_old
EOF
      ;;
    pinned)
      cat <<EOF

To FINISH (once the cause is fixed):
$reconnect
  bash $ROOT/bin/up-infra.sh
  curl -fsS http://127.0.0.1/api/health

To ABORT (the old Caddy config works on the new network too):
$reconnect
$restore_old
EOF
      ;;
  esac
  if [ "${STEP#recreate}" != "$STEP" ]; then
    cat <<EOF

The failure was in recreating the live backend; the edge itself is migrated.
  docker logs --tail 50 proxcloud-$live-backend
If it does not come up and the warm color is healthy, switch to it:
  sudo -u deploy $ROOT/bin/deploy.sh --rollback
then run the next deploy through CI.
EOF
  fi
}

on_error() {
  local rc=$?
  trap - ERR
  {
    printf '\n%s [edge-migrate][FAILED] step "%s" (exit %s)\n' "$(now)" "$STEP" "$rc"
    recovery
  } >&2
  exit "$rc"
}

migrate() {
  local m
  trap on_error ERR

  step "stop the old Caddy (the edge is down from here)"
  compose proxcloud-caddy caddy/docker-compose.yml down

  step "detach the containers from the old $EDGE_NET"
  members=()
  while IFS= read -r m; do members+=("$m"); done < <(edge_members)
  for m in ${members[@]+"${members[@]}"}; do
    docker network disconnect -f "$EDGE_NET" "$m"
    detached+=("$m")
  done

  step "remove the old $EDGE_NET"
  docker network rm "$EDGE_NET" >/dev/null

  step "create $EDGE_NET on the pinned addressing"
  bash "$ROOT/bin/ensure-networks.sh"
  edge_pinned

  step "re-attach the containers"
  for m in ${detached[@]+"${detached[@]}"}; do
    docker network connect "$EDGE_NET" "$m"
    reattached+=("$m")
  done

  step "start the new Caddy"
  bash "$ROOT/bin/up-infra.sh"

  step "verify the edge"
  [ "$(docker inspect -f "{{(index .NetworkSettings.Networks \"$EDGE_NET\").IPAddress}}" proxcloud-caddy)" = "$CADDY_IP" ]
  wait_http http://127.0.0.1/api/health 60
  log "the edge is back: Caddy on $CADDY_IP answers /api/health"

  if [ "$recreate_live" -eq 1 ]; then
    step "recreate the live backend (proxcloud-$live-backend @ $live_ref) with the new TRUSTED_PROXY_CIDRS"
    REGISTRY="$live_registry" IMAGE_REF="$live_ref" \
      compose "proxcloud-$live" "$live/docker-compose.yml" up -d --no-deps --force-recreate backend
    wait_http "http://127.0.0.1:$(backend_port "$live")/api/health" "$HEALTH_TIMEOUT"
    wait_http http://127.0.0.1/api/health 30
    log "proxcloud-$live-backend is healthy again, directly and through Caddy"
  fi
  trap - ERR

  log "migration complete"
  cat <<EOF

Next (runbook Steps 5 and 6):
  - from another LAN machine, curl http://<this guest's LAN IP>/ must be refused
  - sign in through the public URL; Settings -> Sessions must show your real public IP
  - the warm color ($(idle_of "$live")) keeps the old TRUSTED_PROXY_CIDRS until the next deploy replaces it
EOF
  if [ "$recreate_live" -ne 1 ]; then
    echo "  - the live backend was NOT recreated (see the pre-check note): run the next deploy soon"
  fi
}

case "${1:-}" in
  --check)
    precheck
    log "ready — run without --check to migrate"
    ;;
  "")
    precheck
    migrate
    ;;
  *) die "usage: $0 [--check]" ;;
esac
