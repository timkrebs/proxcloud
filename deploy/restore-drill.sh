#!/usr/bin/env bash
# restore-drill — PROVE a pg_dump snapshot round-trips (release-engineer.md, DR).
#
# The disaster-recovery runbook claims a `pg_dump | gzip` snapshot can be
# restored. A restore procedure that has never been exercised is fiction, so this
# drill exercises the EXACT prod snapshot/restore primitive end-to-end against a
# THROWAWAY database and asserts the data survives:
#
#   create scratch DB -> schema + rows (with a checksum)
#     -> pg_dump | gzip           (the same snapshot format deploy.sh writes)
#     -> DROP the schema           (simulate loss)
#     -> gunzip | psql             (the DR restore step)
#     -> assert row count + checksum unchanged
#
# It NEVER touches a dev/staging/prod `proxcloud` DB: it spins its own scratch
# Postgres (a throwaway `postgres:16-alpine` container if Docker is up — same
# image prod runs — else a local `initdb` cluster on a unix socket) and tears it
# down on exit. Idempotent and safe to re-run. Prints PASS/FAIL and is the
# evidence source pasted into docs/runbooks/disaster-recovery.md.
set -euo pipefail

DRILL_DB="restore_drill"                 # NEVER "proxcloud"
DRILL_USER="drill"
ROWS=1000
WORK="$(mktemp -d "${TMPDIR:-/tmp}/proxcloud-restore-drill.XXXXXX")"
DUMP="$WORK/snapshot.sql.gz"
MODE=""
CID=""
PGCTL_DATADIR=""

now()  { date -u +%Y-%m-%dT%H:%M:%SZ; }
log()  { printf '%s [restore-drill] %s\n' "$(now)" "$*"; }
fail() { printf '%s [restore-drill][FAIL] %s\n' "$(now)" "$*" >&2; exit 1; }

cleanup() {
  local rc=$?
  if [ "$MODE" = "docker" ] && [ -n "$CID" ]; then
    docker rm -f "$CID" >/dev/null 2>&1 || true
  elif [ "$MODE" = "initdb" ] && [ -n "$PGCTL_DATADIR" ]; then
    "$PGBIN/pg_ctl" -D "$PGCTL_DATADIR" -m immediate stop >/dev/null 2>&1 || true
  fi
  rm -rf "$WORK" 2>/dev/null || true
  return $rc
}
trap cleanup EXIT

# psql/pgdump are set to arrays that target whichever scratch engine we spun up.
declare -a PSQL PGDUMP

# ── engine A: throwaway docker postgres (preferred; mirrors prod image) ───────
start_docker() {
  local name port img="postgres:16-alpine" i
  name="proxcloud-restore-drill-$$"
  port="55433"
  log "starting throwaway $img container ($name) on 127.0.0.1:$port"
  if ! docker run -d --rm --name "$name" \
        -e POSTGRES_USER="$DRILL_USER" -e POSTGRES_PASSWORD=drill -e POSTGRES_DB=postgres \
        -p "127.0.0.1:$port:5432" "$img" >/dev/null 2>&1; then
    return 1
  fi
  CID="$name"; MODE="docker"
  for i in $(seq 1 60); do
    if docker exec "$name" pg_isready -U "$DRILL_USER" -d postgres >/dev/null 2>&1; then break; fi
    [ "$i" -eq 60 ] && return 1
    sleep 1
  done
  # Run psql/pg_dump INSIDE the container so client/server versions always match.
  PSQL=(docker exec -i "$name" psql -v ON_ERROR_STOP=1 -U "$DRILL_USER")
  PGDUMP=(docker exec "$name" pg_dump -U "$DRILL_USER")
  return 0
}

# ── engine B: local initdb cluster on a private unix socket (fallback) ────────
start_initdb() {
  local i
  PGBIN=""
  for c in /opt/homebrew/opt/postgresql@16/bin /opt/homebrew/opt/postgresql@15/bin \
           /opt/homebrew/opt/postgresql@14/bin /usr/lib/postgresql/16/bin ""; do
    if [ -n "$c" ] && [ -x "$c/initdb" ]; then PGBIN="$c"; break; fi
    if [ -z "$c" ] && command -v initdb >/dev/null 2>&1; then PGBIN="$(dirname "$(command -v initdb)")"; break; fi
  done
  [ -n "$PGBIN" ] || return 1
  PGCTL_DATADIR="$WORK/pgdata"
  local sock="$WORK/sock"
  mkdir -p "$sock"
  log "no docker — using local initdb cluster ($PGBIN)"
  "$PGBIN/initdb" -D "$PGCTL_DATADIR" -U "$DRILL_USER" -A trust >/dev/null 2>&1 || return 1
  "$PGBIN/pg_ctl" -D "$PGCTL_DATADIR" -w -o "-p 55434 -k $sock -c listen_addresses=''" start >/dev/null 2>&1 || return 1
  MODE="initdb"
  for i in $(seq 1 30); do
    if "$PGBIN/pg_isready" -h "$sock" -p 55434 -U "$DRILL_USER" >/dev/null 2>&1; then break; fi
    [ "$i" -eq 30 ] && return 1
    sleep 1
  done
  PSQL=("$PGBIN/psql" -v ON_ERROR_STOP=1 -h "$sock" -p 55434 -U "$DRILL_USER" -d postgres)
  PGDUMP=("$PGBIN/pg_dump" -h "$sock" -p 55434 -U "$DRILL_USER")
  return 0
}

start_engine() {
  if docker info >/dev/null 2>&1 && start_docker; then
    log "engine: docker (postgres:16-alpine)"
  elif start_initdb; then
    log "engine: local initdb"
  else
    fail "could not start any scratch Postgres (docker down and no local initdb)"
  fi
}

q() { "${PSQL[@]}" -d "$DRILL_DB" -tA -c "$1"; }

main() {
  start_engine

  log "create scratch database '$DRILL_DB' (never touches 'proxcloud')"
  "${PSQL[@]}" -d postgres -c "DROP DATABASE IF EXISTS $DRILL_DB;" >/dev/null
  "${PSQL[@]}" -d postgres -c "CREATE DATABASE $DRILL_DB;" >/dev/null

  log "seed schema + $ROWS rows"
  "${PSQL[@]}" -d "$DRILL_DB" >/dev/null <<SQL
CREATE SCHEMA drill;
CREATE TABLE drill.rows (
  id     bigserial PRIMARY KEY,
  tenant text NOT NULL,
  vmid   int  NOT NULL,
  payload text NOT NULL
);
INSERT INTO drill.rows (tenant, vmid, payload)
SELECT 'tenant-' || (g % 7), 99000 + g, md5(g::text)
FROM generate_series(1, $ROWS) AS g;
SQL

  local before_count before_sum
  before_count="$(q "SELECT count(*) FROM drill.rows;")"
  before_sum="$(q "SELECT md5(string_agg(payload, ',' ORDER BY id)) FROM drill.rows;")"
  log "pre-snapshot:  count=$before_count checksum=$before_sum"

  log "pg_dump | gzip  ->  $(basename "$DUMP")   (the exact prod snapshot format)"
  "${PGDUMP[@]}" -d "$DRILL_DB" | gzip >"$DUMP"
  [ -s "$DUMP" ] || fail "snapshot is empty"
  log "snapshot bytes: $(wc -c <"$DUMP" | tr -d ' ')"

  log "simulate loss: DROP SCHEMA drill CASCADE"
  "${PSQL[@]}" -d "$DRILL_DB" -c "DROP SCHEMA drill CASCADE;" >/dev/null
  local gone
  gone="$(q "SELECT to_regclass('drill.rows') IS NULL;")"
  [ "$gone" = "t" ] || fail "schema was not actually dropped — drill would be meaningless"
  log "confirmed: drill.rows is gone"

  log "restore: gunzip | psql   (the DR restore step)"
  gunzip -c "$DUMP" | "${PSQL[@]}" -d "$DRILL_DB" >/dev/null

  local after_count after_sum
  after_count="$(q "SELECT count(*) FROM drill.rows;")"
  after_sum="$(q "SELECT md5(string_agg(payload, ',' ORDER BY id)) FROM drill.rows;")"
  log "post-restore:  count=$after_count checksum=$after_sum"

  [ "$after_count" = "$before_count" ] || fail "row count changed: $before_count -> $after_count"
  [ "$after_sum" = "$before_sum" ]     || fail "checksum changed: data did NOT survive the round-trip"

  log "PASS: $after_count rows survived pg_dump -> drop -> restore, checksums identical"
}
main "$@"
