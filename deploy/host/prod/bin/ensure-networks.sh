#!/usr/bin/env bash
# Ensure the PROD docker networks exist (ADR-0015 §1), with proxcloud-edge on
# its PINNED addressing (ADR-0034). The single place these networks are
# created: bootstrap.sh, up-infra.sh and deploy.sh all call this.
#
# Why pinned: cloudflared runs on the host and reaches Caddy through its
# loopback-published port, so every tunneled request arrives at Caddy from the
# edge network's GATEWAY address. Caddy may only derive the client IP from
# CF-Connecting-IP for that one source, and the backend may only read
# X-Real-IP from Caddy itself — both trust decisions need addresses that
# never change. Values that must agree:
#   EDGE_GATEWAY  -> caddy/Caddyfile        trusted_proxies static 10.254.254.1/32
#   Caddy's IP    -> caddy/docker-compose   ipv4_address 10.254.254.10
#                 -> .env                   TRUSTED_PROXY_CIDRS=10.254.254.10/32
# Dynamic container addresses come only from EDGE_IP_RANGE (the upper half),
# so no redeployed container can ever take Caddy's pinned address.
#
# An existing proxcloud-edge with any other addressing is NOT modified here —
# changing it requires detaching the running stacks. This exits 3 with the
# migration pointer instead, BEFORE the caller touches any container.
set -euo pipefail

EDGE_NET=proxcloud-edge
EDGE_SUBNET=10.254.254.0/24
EDGE_IP_RANGE=10.254.254.128/25
EDGE_GATEWAY=10.254.254.1
DATA_NET=proxcloud-data-net

if ! docker network inspect "$EDGE_NET" >/dev/null 2>&1; then
  docker network create --subnet "$EDGE_SUBNET" --ip-range "$EDGE_IP_RANGE" \
    --gateway "$EDGE_GATEWAY" "$EDGE_NET" >/dev/null
  printf 'ensure-networks: created %s on %s\n' "$EDGE_NET" "$EDGE_SUBNET"
fi

want="$EDGE_SUBNET $EDGE_IP_RANGE $EDGE_GATEWAY"
have="$(docker network inspect "$EDGE_NET" \
  --format '{{range .IPAM.Config}}{{.Subnet}} {{.IPRange}} {{.Gateway}}{{"\n"}}{{end}}')"
if ! printf '%s\n' "$have" | grep -qxF "$want"; then
  {
    printf 'ensure-networks: %s has addressing [%s]\n' "$EDGE_NET" "$(printf '%s' "$have" | tr '\n' ';')"
    printf 'ensure-networks: it must be subnet %s, ip-range %s, gateway %s.\n' \
      "$EDGE_SUBNET" "$EDGE_IP_RANGE" "$EDGE_GATEWAY"
    printf 'ensure-networks: one-time migration required — nothing was changed.\n'
    printf 'ensure-networks: run bin/migrate-edge-network.sh per docs/runbooks/prod-edge-network-migration.md\n'
  } >&2
  exit 3
fi

docker network inspect "$DATA_NET" >/dev/null 2>&1 || docker network create "$DATA_NET" >/dev/null
