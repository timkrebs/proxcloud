#!/bin/sh
# SSH forced-command wrapper for the SOAK key (ADR-0014 §5) — SECURITY-CRITICAL.
#
# A DEDICATED, least-privilege sibling to deploy-wrapper.sh. The soak CI key's
# authorized_keys pins:
#   command="/opt/proxcloud/bin/soak-wrapper.sh",no-port-forwarding,\
#   no-agent-forwarding,no-X11-forwarding,no-pty <ci-soak-key>
#
# Unlike deploy-wrapper.sh this takes NO ref and offers NO deploy/rollback verb:
# it accepts only an empty command or the literal `soak`, and can do exactly ONE
# thing — exec the read-only soak sweep. The soak key therefore CANNOT deploy,
# CANNOT rollback, CANNOT pass any argument; it is strictly MORE locked down than
# the deploy key. soak.sh itself never flips the proxy nor rewrites live-color —
# it only stops the already-retired color past its soak window and prunes images.
# POSIX sh on purpose (tiny, auditable). deploy-wrapper.sh is left UNCHANGED.
set -eu

STATE_LOG=/opt/proxcloud/state/soak-wrapper.log
log()  { printf '%s soak-wrapper: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >>"$STATE_LOG" 2>/dev/null || true; }
deny() { log "DENIED raw=[${SSH_ORIGINAL_COMMAND:-}]"; printf 'denied\n' >&2; exit 2; }

case "${SSH_ORIGINAL_COMMAND:-}" in
  ""|soak)
    log "ACCEPT soak"
    exec /opt/proxcloud/bin/soak.sh
    ;;
  *)
    deny
    ;;
esac
