# Runbook — Failure drills (deliberate breakage)

> A rollback / abort path that has never fired for real is fiction
> (release-engineer.md). This runbook lists the three deliberate breakages, the
> exact command to inject each, the pipeline behavior to expect, and the
> observable evidence to capture. Two of the three have a part that is **runnable
> today with no live guest** — those are run below with real output. The parts
> that genuinely need the provisioned guests are written precisely and marked
> **execute once staging is live — evidence pending**; no evidence is fabricated.

**The one honest caveat:** the full end-to-end abort/rollback under live traffic
has not fired on a real guest yet, because no live environment exists (Tim has
not `terraform apply`-d). That last confirmation is the one thing gated on the
apply. The *mechanisms* below (non-zero migrate exit; the symlink flip) are
proven; their *integration under real containers + live SSE/WS* is pending.

---

## Drill 1 — Kill the backend mid-deploy

**Goal:** prove a backend that dies during a deploy is caught by the health gate
and **never cuts over** (prod) / fails the deploy loudly (staging).

**Inject (on the guest, during the deploy window — the idle backend is up but the
health gate is still polling):**

```bash
# prod: the idle color's backend (blue shown; use green if green is idle)
docker kill proxcloud-blue-backend
# staging:
docker kill proxcloud-staging-backend
```

**Expected pipeline behavior:**
- `wait_health` never sees `/api/v1/version == SHA`; after `HEALTH_TIMEOUT` it
  returns non-zero.
- `deploy.sh` tees the backend logs to `state/last-migrate.log` and calls `die` →
  script exits non-zero → the deploy job fails.
- **Prod:** the failure is **before** the atomic symlink flip, so the old color
  stays live — no rollback needed, no user impact. ntfy posts a high-priority
  `staging/prod deploy FAILED` line.
- **Staging:** deploy fails; `smoke-staging` never runs; the wave stops before the
  prod gate.

**Evidence to capture:** the `die` line + `HEALTH_TIMEOUT` in the job log; tail of
`state/last-migrate.log`; `state/live-color` **unchanged**; the ntfy line.

**Status: execute once staging is live — evidence pending.** (Needs real
containers; do not fabricate.)

---

## Drill 2 — Make a migration fail

**Goal:** prove a failed migration aborts the deploy **before** cutover and never
leaves a half-migrated prod live.

### 2a. Mechanism — RUNNABLE NOW (real evidence)

The load-bearing claim is: **`proxcloud migrate` exits non-zero when it cannot
apply migrations**, so both the one-shot migrator compose service and the
boot-migrate path fail the deploy. Proven against a broken/unreachable DB (a
migration that cannot even connect is the strongest form of "migration fails"):

```
$ env -i PATH=... \
    PROXMOX_URL=https://127.0.0.1:8006 PROXMOX_TOKEN_ID='drill@pam!x' PROXMOX_TOKEN_SECRET=drill \
    SECRETS_KEY=<32-byte-hex> \
    DATABASE_URL='postgres://proxcloud:proxcloud@127.0.0.1:1/proxcloud?sslmode=disable' \
    ./proxcloud migrate
time=2026-08-12T23:17:56Z level=ERROR msg="migrate failed" stage=datastore \
  err="store: ping: failed to connect to `user=proxcloud database=proxcloud`: 127.0.0.1:1 (127.0.0.1): dial error: dial tcp 127.0.0.1:1: connect: connection refused"
----
proxcloud migrate EXIT CODE = 1
RESULT: non-zero as required -> deploy.sh migrate step would 'die' and ABORT the cutover
```

In `deploy.sh`, that non-zero exit is what makes the `migrator` `docker compose
run --rm migrator` (or the boot-migrate health gate) fail, so `migrate_idle`
calls `die` and the script exits **before** `switch_to` — the old color stays
live. Mechanism proven.

### 2b. Full path — pending live

**Inject:** add a deliberately-broken migration to `backend/migrations/` (e.g. a
`CREATE TABLE` referencing a non-existent type, or a syntactically invalid
statement), build/publish, and deploy to staging.

**Expected:** the migrator service (`USE_MIGRATOR_SERVICE=1`) or the backend at
boot logs the migration error and exits non-zero; `deploy.sh` `die`s; the deploy
job fails; on prod nothing cuts over. `state/last-migrate.log` holds the captured
migrator output. The `pg_dump` snapshot taken **before** the migration is present
in `data/snapshots/`.

**Status: execute once staging is live — evidence pending** for the full compose
migrator path (2a proves the exit-code contract it relies on).

---

## Drill 3 — Kill the proxy during cutover

**Goal:** prove the cutover / rollback around the atomic Caddy switch behaves
sanely when the proxy dies mid-flip.

### 3a. Mechanism — RUNNABLE NOW (real evidence)

Rollback and cutover are the **same primitive**: `ln -sfn <color>.caddy
active.caddy` + `caddy reload`. The symlink-flip half is exercised by sourcing the
**real** prod `deploy.sh` (ROOT rewritten to a `/tmp` sandbox, final `main "$@"`
stripped) and driving its actual `do_rollback` — only `reload_caddy` (docker) and
the warm-color health `curl` are stubbed; `read_live`, `idle_of`, `backend_port`,
`switch_to`'s `ln -sfn`, and the `state/` writes are the real code:

```
INITIAL     active.caddy -> blue.caddy   live-color=blue
--- do_rollback #1 (expect blue -> green) ---
2026-08-12T21:19:24Z [deploy] rollback: current live=blue -> warm=green; verifying warm color on :28080
    [stub] caddy reload (no docker in sandbox)
2026-08-12T21:19:25Z [deploy] rollback complete: green now live (was blue)
AFTER RB #1 active.caddy -> green.caddy   live-color=green
--- do_rollback #2 (expect green -> blue) ---
2026-08-12T21:19:25Z [deploy] rollback: current live=green -> warm=blue; verifying warm color on :18080
    [stub] caddy reload (no docker in sandbox)
2026-08-12T21:19:25Z [deploy] rollback complete: blue now live (was green)
AFTER RB #2 active.caddy -> blue.caddy   live-color=blue
last-cutover marker: blue 2026-08-12T21:19:25Z
PASS: active.caddy flips blue<->green atomically and live-color tracks it
```

The flip is atomic (`ln -sfn` = `rename(2)`), reversible, and `live-color` /
`last-cutover` track it — exactly what `soak.yml` later reads.

### 3b. Full path — pending live

**Inject** (during a real cutover, right as `switch_to` runs):

```bash
docker kill proxcloud-caddy
```

**Expected / to observe (do NOT assume — capture what actually happens):**
`switch_to` flips the symlink, then `reload_caddy` runs `caddy reload` against a
dying/dead proxy. The reload command fails → `deploy.sh` `die`s. The Caddy
container's `restart: unless-stopped` policy restarts it; on restart Caddy reads
`active.caddy`, which was **already flipped to the new color**. Net effect to
verify on the guest: after Caddy restarts, is the new color serving? Is
`state/live-color` consistent with `active.caddy`? Does a manual `deploy.sh
--rollback` cleanly recover if not? Capture the caddy container logs, the
`active.caddy` target, `state/live-color`, and the public `/api/v1/version`.

**Status: execute once staging/prod is live — evidence pending.** This is the
drill most worth running for real, because the "proxy dies exactly at the flip"
timing is the one the sandbox cannot reproduce.

---

## Summary

| Drill | Mechanism proven now | Full path (live) |
|-------|----------------------|------------------|
| 1 — kill backend mid-deploy | — | **pending** |
| 2 — migration fails | ✅ `migrate` exits 1 (real output above) | **pending** (compose migrator path) |
| 3 — kill proxy at cutover | ✅ `do_rollback` flips `active.caddy` blue↔green (real output above) | **pending** (proxy-dies-at-flip timing) |

Re-run the runnable drills anytime: `make restore-drill` (data round-trip,
`disaster-recovery.md`), the `proxcloud migrate` invocation above (2a), and the
sandbox `do_rollback` harness (3a). Run the three **pending** live drills on the
first staging bring-up and paste the observed evidence back into this file —
until then, the abort/rollback paths are proven in mechanism but not yet in
production reality.
