#!/usr/bin/env bash
# Idempotent on-guest bootstrap for the PROD guest (ADR-0015 §2). Run once by
# Terraform after first-boot.sh, and safe to re-run by hand. Creates:
#   - the two EXTERNAL docker networks (proxcloud-edge, proxcloud-data-net),
#   - the /opt/proxcloud/{state,data/snapshots,data/tls,caddy/upstream} tree,
#   - the Postgres TLS cert (self-signed; sslmode=require),
#   - the active.caddy symlink -> the live color's upstream file,
#   - the deploy user's forced-command authorized_keys (from ci-deploy-key.pub),
#   - hardened ownership (bin/ root-owned, state/data/caddy deploy-owned).
# It does NOT bring any stack up — that needs the manually-placed .env
# (use bin/up-infra.sh once .env is in place; deploy.sh also ensures infra).
set -euo pipefail

ROOT=/opt/proxcloud
DEPLOY_USER=deploy

if [ "$(id -u)" -ne 0 ]; then
  exec sudo -n "$0" "$@"
fi

# 1. external docker networks (colors + caddy + postgres attach by name), with
#    proxcloud-edge on its pinned addressing. An existing edge network with
#    other addressing (exit 3) needs a one-time migration: finish every other
#    step, then fail loudly at the end so the provisioning run shows it.
networks_status=0
bash "$ROOT/bin/ensure-networks.sh" || networks_status=$?

# 2. directory tree
mkdir -p "$ROOT/state" "$ROOT/data/snapshots" "$ROOT/data/tls" "$ROOT/caddy/upstream"

# 3. seed live-color if absent (EMPTY => the first deploy targets blue)
[ -f "$ROOT/state/live-color" ] || : >"$ROOT/state/live-color"

# 4. active.caddy -> the LIVE color's upstream file (relative symlink). The
#    link is never provisioned (gitignored) and a re-run must never re-point a
#    live edge: it follows state/live-color, and only a fresh guest (empty
#    live-color, no link yet) gets blue.
link="$ROOT/caddy/upstream/active.caddy"
live="$(tr -d '[:space:]' <"$ROOT/state/live-color")"
case "$live" in
  blue | green)
    if [ "$(readlink "$link" 2>/dev/null || true)" != "$live.caddy" ]; then
      ln -sfn "$live.caddy" "$link"
      echo "bootstrap: caddy/upstream/active.caddy -> $live.caddy (the live color)"
    fi
    ;;
  "") [ -L "$link" ] || ln -s blue.caddy "$link" ;;
  *) echo "bootstrap: WARNING state/live-color is '$live' — caddy/upstream/active.caddy left as is" >&2 ;;
esac

# 5. deploy user's forced-command authorized_keys (public keys => safe to handle)
#    Two DISTINCT keys, two DISTINCT forced commands (ADR-0014 §5/§7):
#      - ci-deploy-key -> deploy-wrapper.sh  (deploy <ref> | rollback)
#      - ci-soak-key   -> soak-wrapper.sh    (soak-only; no ref, no rollback —
#                         strictly MORE locked down than the deploy key)
#    The soak key exists so the hourly, unattended soak.yml never needs the
#    prod-environment-gated deploy key (which would demand approval every hour).
if [ -f "$ROOT/ci-deploy-key.pub" ] && [ -s "$ROOT/ci-deploy-key.pub" ]; then
  install -d -m 700 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "/home/$DEPLOY_USER/.ssh"
  ak="/home/$DEPLOY_USER/.ssh/authorized_keys"
  no_pty='no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty'
  deploy_key="$(cat "$ROOT/ci-deploy-key.pub")"
  {
    printf 'command="/opt/proxcloud/bin/deploy-wrapper.sh",%s %s\n' "$no_pty" "$deploy_key"
    if [ -f "$ROOT/ci-soak-key.pub" ] && [ -s "$ROOT/ci-soak-key.pub" ]; then
      soak_key="$(cat "$ROOT/ci-soak-key.pub")"
      printf 'command="/opt/proxcloud/bin/soak-wrapper.sh",%s %s\n' "$no_pty" "$soak_key"
    fi
  } >"$ak"
  chown "$DEPLOY_USER:$DEPLOY_USER" "$ak"
  chmod 600 "$ak"
  if [ -f "$ROOT/ci-soak-key.pub" ] && [ -s "$ROOT/ci-soak-key.pub" ]; then
    echo "bootstrap: forced-command authorized_keys installed for $DEPLOY_USER (deploy + soak keys)"
  else
    echo "bootstrap: forced-command authorized_keys installed for $DEPLOY_USER (deploy key only — no ci-soak-key.pub yet; soak.yml will be denied until it is added)"
  fi
else
  echo "bootstrap: WARNING no ci-deploy-key.pub — deploy user has no CI key yet"
fi

# 6. ownership / hardening
#    bin/ root-owned & non-writable by deploy so the forced-command target and
#    deploy.sh cannot be rewritten from the deploy session; everything the
#    deploy user must write (state, snapshots, the caddy symlink) is deploy-owned.
chown -R root:root "$ROOT/bin"
chmod 0755 "$ROOT/bin"/*.sh
chown -R "$DEPLOY_USER:$DEPLOY_USER" \
  "$ROOT/state" "$ROOT/caddy" "$ROOT/blue" "$ROOT/green" "$ROOT/data/snapshots"
if [ -f "$ROOT/.env" ]; then
  chown "$DEPLOY_USER:$DEPLOY_USER" "$ROOT/.env"
  chmod 600 "$ROOT/.env"
fi

# 7. Postgres TLS cert LAST so its 70:70 ownership is not clobbered by the chowns
"$ROOT/bin/gen-postgres-cert.sh" "$ROOT/data/tls"

case "$networks_status" in
  0) ;;
  3)
    echo "prod bootstrap: complete EXCEPT the edge network — migrate it (bin/migrate-edge-network.sh, docs/runbooks/prod-edge-network-migration.md); deploys refuse until then" >&2
    exit 3
    ;;
  *)
    echo "prod bootstrap: complete EXCEPT the docker networks — ensure-networks.sh failed (exit $networks_status, see above)" >&2
    exit "$networks_status"
    ;;
esac
echo "prod bootstrap: complete"
