#!/bin/sh
# SSH forced-command wrapper (ADR-0015 §3) — SECURITY-CRITICAL.
#
# The `deploy` user's authorized_keys pins:
#   command="/opt/proxcloud/bin/deploy-wrapper.sh",no-port-forwarding,\
#   no-agent-forwarding,no-X11-forwarding,no-pty <ci-deploy-key>
#
# The CI deploy key can therefore do NOTHING but trigger a validated
# deploy/rollback. This wrapper:
#   - trusts ONLY $SSH_ORIGINAL_COMMAND (never argv, never the environment),
#   - allows exactly two verbs: `deploy <ref>` and `rollback`,
#   - regex-validates <ref> (40-hex git SHA or vMAJOR.MINOR.PATCH),
#   - execs deploy.sh with the validated arg — never a shell, never eval.
# Anything else is denied with exit 2. POSIX sh on purpose (tiny, auditable).
set -eu

STATE_LOG=/opt/proxcloud/state/deploy-wrapper.log

log()  { printf '%s wrapper: %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >>"$STATE_LOG" 2>/dev/null || true; }
deny() { log "DENIED ($*) raw=[${SSH_ORIGINAL_COMMAND:-}]"; printf 'denied: %s\n' "$*" >&2; exit 2; }

cmd="${SSH_ORIGINAL_COMMAND:-}"

# Disable pathname expansion so a `*`/`?` in the request can never glob, then
# split strictly on IFS whitespace into positional parameters. No eval, no
# command substitution, no subshell ever touches the untrusted string.
set -f
# shellcheck disable=SC2086 # deliberate word-split of the untrusted command
set -- $cmd

verb="${1:-}"
case "$verb" in
  deploy)
    [ "$#" -eq 2 ] || deny "deploy takes exactly one argument"
    ref="$2"
    if printf '%s' "$ref" | grep -Eq '^([0-9a-f]{40}|v[0-9]+\.[0-9]+\.[0-9]+)$'; then
      log "ACCEPT deploy $ref"
      exec /opt/proxcloud/bin/deploy.sh "$ref"
    fi
    deny "invalid ref"
    ;;
  rollback)
    [ "$#" -eq 1 ] || deny "rollback takes no argument"
    log "ACCEPT rollback"
    exec /opt/proxcloud/bin/deploy.sh --rollback
    ;;
  *)
    deny "unknown verb"
    ;;
esac
