# Runbook — Rollback

**Rollback in prod is a proxy switch, never a down-migration.** The old color
stays warm after every cutover, so reverting is one `caddy reload` back to it
(ADR-0015). Down-migrations exist for **dev only** — never run one against prod.

## Two ways a rollback happens

### 1. Automatic (prod smoke failed the live path)

`deploy-prod`'s `smoke-prod` step runs the smoke binary through the **public**
URL after the cutover. If it fails, the job's `Auto-rollback` step fires — it
SSHes `rollback` to the forced-command wrapper, which flips `active.caddy` back
to the warm previous color and `caddy reload`s. ntfy posts a high-priority
`ROLLED BACK` line naming the failed assertion. Nothing for you to do except
investigate; realistic RTO ≈ the smoke window + one reload (single-digit minutes).

### 2. Manual (a bad build caught later, by a human)

Same primitive, either direction:

```bash
# From CI / your laptop, through the locked forced command:
ssh <prod-deploy-host> rollback

# Or on the prod guest directly:
/opt/proxcloud/bin/deploy.sh --rollback
```

`deploy.sh --rollback` reads `state/live-color`, picks the warm color, **verifies
that color is still healthy on its loopback port** (refuses to flip to a dead
color), flips the symlink, reloads Caddy, and rewrites `state/live-color` +
`state/last-cutover`. It is idempotent and safe to re-run.

> Staging has **no** rollback (`deploy.sh --rollback` is rejected there). Staging
> is disposable — rebuild it instead (`staging-rebuild.md`).

## When a DB restore is justified INSTEAD of a proxy switch

A proxy switch reverts **code**. It does **not** undo data changes. Restore the
database **only** when a migration actually **corrupted or destroyed data** that
the expand/contract discipline should have prevented — never for a routine bad
deploy.

Why this is rare by construction:
- Migrations are **expand → migrate → contract**, backward-compatible for one
  version, so the warm old color runs against the new schema during soak. A code
  rollback therefore keeps working against the migrated schema.
- **Destructive steps (drop column/table) ship one release after the code stops
  using them** — the PR description states which release may contract. So the
  release you are rolling back has **not** dropped anything the old color needs.
- Every prod migration is preceded by an automatic `pg_dump` snapshot
  (`/opt/proxcloud/data/snapshots/pre-deploy-*.sql.gz`, retain `SNAPSHOT_RETAIN`).

If (and only if) a migration mangled data: the restore procedure and its proven
drill live in `disaster-recovery.md`. Take the platform offline for the restore
window — a restore is not zero-downtime like a proxy switch.

## Why down-migrations are dev-only

A down-migration in prod would run destructive DDL against live data under time
pressure, is rarely tested against real data shapes, and races the warm old
color that is still reading the schema. The safe, reversible primitive is the
proxy switch; the safety net for genuine data loss is the snapshot restore. Down
files exist so developers can reset a local DB — that is their only job.

## Drill status (honest)

The **symlink-flip half** of rollback is exercised and proven (see
`failure-drills.md` — real `do_rollback` flips `active.caddy` blue↔green in a
sandbox). The **full end-to-end rollback under live traffic** (real `caddy
reload` draining in-flight SSE/WS on the prod guest) is **pending until staging
is live**. A rollback that has never fired for real is not yet fully proven; this
is the one thing gated on Tim's `terraform apply`.
