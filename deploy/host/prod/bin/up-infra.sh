#!/usr/bin/env bash
# Bring up the environment-independent PROD infrastructure: the shared Postgres
# (proxcloud-data) and Caddy (proxcloud-caddy). Run once after placing the real
# /opt/proxcloud/.env, before the first deploy. Idempotent; deploy.sh also
# ensures these are up on every run, so this is just the first-time convenience.
set -euo pipefail

ROOT=/opt/proxcloud
[ -f "$ROOT/.env" ] || { echo "FATAL: $ROOT/.env missing — copy .env.example and fill it" >&2; exit 1; }

docker network inspect proxcloud-edge     >/dev/null 2>&1 || docker network create proxcloud-edge
docker network inspect proxcloud-data-net >/dev/null 2>&1 || docker network create proxcloud-data-net

docker compose --env-file "$ROOT/.env" -p proxcloud-data  -f "$ROOT/data/docker-compose.yml"  up -d
docker compose --env-file "$ROOT/.env" -p proxcloud-caddy -f "$ROOT/caddy/docker-compose.yml" up -d

echo "up-infra: postgres + caddy running"
