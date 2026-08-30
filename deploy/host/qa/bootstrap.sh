#!/usr/bin/env bash
# Idempotent on-guest bootstrap for the QA guest. Simpler than prod: one
# compose stack, no external networks, no color state. Creates the state + tls
# dirs, the Postgres TLS cert, the deploy user's forced-command authorized_keys,
# and hardens ownership. Does NOT bring the stack up (needs the manual .env;
# use deploy.sh once .env is placed). Safe to re-run.
set -euo pipefail

ROOT=/opt/proxcloud
DEPLOY_USER=deploy

if [ "$(id -u)" -ne 0 ]; then
  exec sudo -n "$0" "$@"
fi

mkdir -p "$ROOT/state" "$ROOT/tls"

# Service-catalog snippet-writer credentials dir (ADR-0025). Backend-only,
# bind-mounted read-only. The catalog is OFF by default, so this is empty until
# an operator enables it (docs/runbooks/enable-service-catalog-qa.md). Create it
# owned by 65532 (distroless nonroot backend UID) mode 0500 so only that UID (and
# root, who places the files) can traverse it; the operator drops in id_ed25519 +
# known_hosts and sets chown 65532:65532 / chmod 400 on the key. Idempotent.
install -d -m 0500 -o 65532 -g 65532 "$ROOT/snippet-writer"

# Postgres TLS cert (self-signed; sslmode=require) — 70:70 / 600, see script.
"$ROOT/bin/gen-postgres-cert.sh" "$ROOT/tls"

# deploy user's forced-command authorized_keys (public key => safe to handle)
if [ -f "$ROOT/ci-deploy-key.pub" ] && [ -s "$ROOT/ci-deploy-key.pub" ]; then
  install -d -m 700 -o "$DEPLOY_USER" -g "$DEPLOY_USER" "/home/$DEPLOY_USER/.ssh"
  key="$(cat "$ROOT/ci-deploy-key.pub")"
  opts='command="/opt/proxcloud/bin/deploy-wrapper.sh",no-port-forwarding,no-agent-forwarding,no-X11-forwarding,no-pty'
  printf '%s %s\n' "$opts" "$key" >"/home/$DEPLOY_USER/.ssh/authorized_keys"
  chown "$DEPLOY_USER:$DEPLOY_USER" "/home/$DEPLOY_USER/.ssh/authorized_keys"
  chmod 600 "/home/$DEPLOY_USER/.ssh/authorized_keys"
  echo "bootstrap: forced-command authorized_keys installed for $DEPLOY_USER"
else
  echo "bootstrap: WARNING no ci-deploy-key.pub — deploy user has no CI key yet"
fi

# ownership: bin/ root-owned (forced-command target not rewritable from the
# deploy session), everything else the deploy user must touch is deploy-owned.
chown -R root:root "$ROOT/bin"
chmod 0755 "$ROOT/bin"/*.sh
chown -R "$DEPLOY_USER:$DEPLOY_USER" "$ROOT/state" "$ROOT/caddy"
if [ -f "$ROOT/.env" ]; then
  chown "$DEPLOY_USER:$DEPLOY_USER" "$ROOT/.env"
  chmod 600 "$ROOT/.env"
fi

echo "qa bootstrap: complete"
