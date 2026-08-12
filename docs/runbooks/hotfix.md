# Runbook — Hotfix release (expedited wave by SHA/tag)

When a fix is already on `main` (and `publish.yml` has pushed its `:<SHA>` image)
but you do not want to wait for / trigger a fresh merge, deploy that exact commit
directly. A hotfix is **faster to start, not less gated** — it still runs staging
smoke and still stops at the `production` reviewer gate.

## Preconditions

- The fix commit is on `main` and green in `ci.yml`.
- `publish.yml` has pushed `ghcr.io/timkrebs/proxcloud-{backend,frontend}:<40-hex-SHA>`
  (immutable). Confirm the package version exists before deploying it.
- For a tagged hotfix, the `vX.Y.Z` tag is pushed and its `:<semver>` image exists.

## Steps

1. GitHub → **Actions → `deploy` → Run workflow**.
2. `ref` input = the **full 40-char SHA** (or a `vX.Y.Z` tag). This is
   `workflow_dispatch`; the value is regex-validated on the runner
   (`^[0-9a-f]{40}$` or `^v\d+\.\d+\.\d+$`) and re-validated server-side by the
   forced-command wrapper — a malformed ref never reaches SSH.
3. Run. The wave is the same as a normal release: staging → staging smoke →
   **production gate (approve as timkrebs)** → blue/green cutover → prod smoke →
   auto-rollback on failure.

## When a hotfix is really a rollback

If the "fix" is simply *go back to the last-good build*, you do **not** need a
new image at all — the previous color is warm. Prefer a proxy switch-back (see
`rollback.md`): `ssh <prod-deploy-host> rollback` (forced-command wrapper) or, on
the guest, `/opt/proxcloud/bin/deploy.sh --rollback`. That is seconds, not a wave.

Deploy a *forward* hotfix ref only when the bug needs new code, not when the
previous color already has the good code.

## Guardrails that still apply

- Immutable ref only — never `latest`, never a branch name.
- The `production` gate is not skippable; the approval is the deploy.
- Migrations still follow expand/contract discipline (`rollback.md`). A hotfix
  that ships a destructive migration is not a hotfix — split it.

## Verify after

Same as `release.md`: `curl /api/v1/version` on the public URL, check
`state/live-color` / `state/last-cutover`, and read the ntfy line for the
approver + smoke result.
