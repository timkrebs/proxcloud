# Proxmox VE guest lifecycle semantics

Reference for the Proxcloud job scheduler (auto-shutdown, auto-start, TTL stop,
TTL delete). Documents the exact stop / start / destroy semantics for **qemu**
(VMs) and **lxc** (containers).

- **Target version:** PVE 8.x and 9.x. The Proxcloud server currently runs
  **9.2.4**. Endpoints and parameters below are stable across 8.x/9.x; anything
  version-sensitive is flagged inline.
- **Scope:** documentation only. The scheduler code lives elsewhere; this file
  is the contract it codes against.
- **Grounding in existing code:** `backend/internal/proxmox/guests_tasks.go`
  already implements `GuestStatus` (`GET .../status/current`), `GuestAction`
  (allow-list `start|stop|shutdown|reboot|reset`, **empty JSON body**),
  `DeleteGuest` (`DELETE .../{vmid}` with optional `?purge=1&destroy-unreferenced-disks=1`),
  and the UPID/task model (`TaskStatus`, `exitstatus`). The scheduler builds on
  these primitives.

Verified against:
- API viewer: <https://pve.proxmox.com/pve-docs/api-viewer/>
- `qm(1)`: <https://pve.proxmox.com/pve-docs/qm.1.html>
- `pct(1)`: <https://pve.proxmox.com/pve-docs/pct.1.html>
- Privileges: <https://pve.proxmox.com/wiki/User_Management> ("Permission Management")

---

## 0. Endpoint summary

All paths are prefixed with `/api2/json`. `{t}` is `qemu` or `lxc`.

| Method | Path | Key params | Sync / async | Privilege |
|--------|------|-----------|--------------|-----------|
| GET  | `/nodes/{node}/{t}/{vmid}/status/current` | — | **sync** (returns status object) | `VM.Audit` |
| POST | `/nodes/{node}/{t}/{vmid}/status/start`    | (qemu) `timeout`, `skiplock` | **async → UPID** | `VM.PowerMgmt` |
| POST | `/nodes/{node}/{t}/{vmid}/status/shutdown` | `timeout`, `forceStop`, (qemu also) `keepActive`, `skiplock` | **async → UPID** | `VM.PowerMgmt` |
| POST | `/nodes/{node}/{t}/{vmid}/status/stop`     | `overrule-shutdown`, (qemu) `timeout`, `keepActive`, `skiplock` | **async → UPID** | `VM.PowerMgmt` |
| POST | `/nodes/{node}/{t}/{vmid}/status/reboot`   | `timeout` | **async → UPID** | `VM.PowerMgmt` |
| DELETE | `/nodes/{node}/{t}/{vmid}`               | `purge`, `destroy-unreferenced-disks`, (lxc) `force`, `skiplock` | **async → UPID** | `VM.Allocate` (+ see §4) |
| GET  | `/nodes/{node}/tasks/{upid}/status`       | — | **sync** (`status`, `exitstatus`) | task owner / `VM.Audit` on the guest |

"async → UPID" means the POST/DELETE returns a bare UPID string in `data`; the
real work runs as a node task whose success/failure is only knowable by polling
the task-status endpoint (§2).

---

## 1. Graceful shutdown vs. force stop

### VM (qemu)

- **`POST .../status/shutdown`** — graceful. Issues a QEMU Guest Agent shutdown
  if the agent is configured *and running*, otherwise an **ACPI powerdown**
  event, then waits for the guest OS to power off. Returns a UPID immediately;
  the task stays "running" until the guest is down (or the task's own timeout
  fires).
  - `timeout` `<integer>` — "Wait maximal timeout seconds." If the guest has not
    powered off within `timeout`, the task ends.
  - `forceStop` `<boolean>` (default `0`) — "Make sure the VM stops." When set,
    after `timeout` expires PVE escalates to a hard stop instead of failing.
  - `keepActive` `<boolean>` (default `0`) — do not deactivate storage volumes.
  - **Gotcha:** if `agent: 1` is set in the VM config but the agent is *not*
    actually running inside the guest, some qemu-server versions do **not** fall
    back to ACPI and the shutdown hangs until timeout. Do not assume graceful
    shutdown works just because the config enables the agent.
- **`POST .../status/stop`** — immediate hard kill (equivalent to pulling the
  power cord); the guest OS gets no chance to flush. Params: `timeout`,
  `keepActive`, `skiplock` (root-only), and `overrule-shutdown` `<boolean>`
  (default `0`) — "Try to abort active qmshutdown tasks before stopping."
  `overrule-shutdown` matters when a graceful shutdown task is already in flight
  and holding the guest lock (see §5).

### Container (lxc)

- **`POST .../status/shutdown`** — graceful lxc shutdown; triggers the
  container's init/OS shutdown.
  - `timeout` `<integer>` (default **60**) — "Wait maximal timeout seconds."
    Note the LXC default (60) differs from VM shutdown (no fixed default).
  - `forceStop` `<boolean>` (default `0`) — "Make sure the Container stops."
    After `timeout` PVE force-stops the container.
- **`POST .../status/stop`** — immediate stop of the container. Params:
  `overrule-shutdown` (default `0`, same meaning as VM), `skiplock` (root-only).
  **The LXC `stop` endpoint has no `timeout` param** (a hard stop needs none).

### Proxcloud's chosen strategy: poll-then-force (recommended)

Proxcloud's plan — *graceful `shutdown`, poll `status/current` up to a
configurable grace period (default 120s), then force `stop` if still running* —
is **sound and preferred** over delegating to PVE via `forceStop=1&timeout=120`.
Rationale:

| Approach | Pro | Con |
|----------|-----|-----|
| Proxcloud poll-then-force | Full visibility of *why/when* the guest stopped; can emit progress + audit at each phase; single, uniform grace policy across qemu/lxc; can abort/adjust; not blocked by PVE holding a long-running shutdown task | Two API round-trips + polling; must handle the interleaving in §5 |
| `forceStop=1&timeout=N` to PVE | One call; PVE owns the escalation | The shutdown task holds the guest **lock** for the whole window, so a later `stop` may hit `overrule-shutdown` needs; opaque to Proxcloud (single UPID either way, but you can't distinguish "shut down cleanly" from "was force-killed at t=120" without parsing the task log); LXC and VM defaults differ |

**Recommendation:** keep `shutdown` calls *without* `forceStop` (send an explicit
`timeout` at least as long as the poll grace, e.g. `timeout=125` when grace=120,
so PVE's task does not die and drop the guest lock before Proxcloud's poll loop
finishes), poll `status/current`, and issue an explicit `stop` when the grace
period is exceeded. Because the graceful `shutdown` task holds the guest lock,
the follow-up `stop` should send **`overrule-shutdown=1`** so it can abort the
still-running shutdown task rather than failing on the lock.

> **Note on the current code:** `GuestAction` posts an **empty body** and does
> not expose `timeout` / `forceStop` / `overrule-shutdown`. To send these the
> scheduler needs either a new client method or a raw `POST` with the params in
> the request body (or query string). The go-proxmox library's
> `VirtualMachine.Shutdown/Stop` / `Container.Shutdown/Stop` helpers also post
> without exposing these params, so **raw `/api2/json` is required** to control
> `timeout`/`forceStop`/`overrule-shutdown`.

---

## 2. Detecting stopped vs. running

Two independent signals — do not conflate them:

1. **Guest run-state:** `GET /nodes/{node}/{t}/{vmid}/status/current` →
   `status` is `"running"` or `"stopped"` (also returns `qmpstatus`, `lock`,
   `uptime`, etc.). This is the *authoritative* state and the one the scheduler
   must gate on. Proxcloud already decodes this in `GuestStatus`
   (`guestStatusWire.Status`). Poll it on a fixed interval (e.g. every 2–3s)
   during the grace window.
   - The `lock` field (e.g. `"backup"`, `"migrate"`, `"snapshot"`,
     `"clone"`) tells you the guest is busy and power/destroy ops will fail —
     check it before acting (see §5).
2. **Task completion:** the `shutdown`/`stop`/`start`/`delete` POST/DELETE
   returns a UPID. `GET /nodes/{node}/tasks/{upid}/status` reports
   `status: "running"` while in flight, then `status: "stopped"` with
   `exitstatus`. **Success ⇔ `exitstatus == "OK"`**; anything else is the
   failure message. Proxcloud already models this in `TaskStatus` /
   `taskWire.info()` (normalizes `EndTime==0 ⇔ running`).

**How they relate:** a `shutdown` task reaching `exitstatus: OK` means "the
graceful request completed" — but with `forceStop=0` an OK task can still leave
a VM *running* if the OS ignored ACPI within the task timeout. Therefore the
scheduler must treat **`status/current.status == "stopped"` as the success
condition**, and use the task's `exitstatus` only to detect hard *errors*
(lock conflicts, permission, etc.). Do not declare "stopped" solely from
`exitstatus: OK`.

---

## 3. Start

- **`POST .../status/start`** → UPID. Precondition: the guest must be
  **stopped**. For qemu, `timeout` `<integer>` defaults to
  `max(30, VM memory in GiB)`; `skiplock` is root-only. For lxc there is no
  `timeout` param (there is a `debug` flag).
- **Already running:** starting a running guest is **not** a silent no-op — PVE
  creates the task and the task **fails**, with `exitstatus` like
  `VM is already running` / `CT is already running`. The initial POST may still
  return a UPID (HTTP 200); the error surfaces via task status. The scheduler
  must therefore check `status/current` first and skip the start if already
  `running` (treat as success — desired state reached).
- A guest that is **locked** will fail to start with a lock error (§5).

---

## 4. Destroy

- **`DELETE /nodes/{node}/{t}/{vmid}`** → UPID. Runs as an async task that
  removes the guest config and its **referenced disks** from storage.
- **Precondition — must be stopped.**
  - **qemu:** a running VM cannot be destroyed; the task fails with an error
    (e.g. lock/running). There is no `--force` on `qm destroy` to kill+destroy
    in one step, so the scheduler must stop first (§5 sequence).
  - **lxc:** `pct destroy` *does* accept **`force` `<boolean>`** (default `0`) —
    "Force destroy, even if running." So an LXC can be destroyed while running
    with `force=1`. Prefer an explicit stop first anyway, for clean, auditable
    phases and parity with VMs.
- **Params:**
  - `purge` `<boolean>` — remove the VMID from *related configurations* (backup
    jobs, replication jobs, HA). For **lxc**, "Related ACLs and Firewall entries
    will always be removed" regardless; `purge` covers backup/replication/HA.
  - `destroy-unreferenced-disks` `<boolean>` (default `0`) — additionally destroy
    disks on enabled storages that carry this VMID but are **not** referenced in
    the config (orphans from failed edits, detached disks, etc.).
  - `skiplock` (root-only); lxc `force` as above.
  - Proxcloud's `DeleteGuest(purge=true)` already appends
    `?purge=1&destroy-unreferenced-disks=1`, which is the correct "leave nothing
    behind" TTL-delete shape.
- **Disk removal:** yes — a normal destroy removes the disks referenced in the
  config. Unreferenced same-VMID orphans are only removed with
  `destroy-unreferenced-disks=1`.
- **Locked guest:** if the guest holds a lock (backup/migrate/snapshot), destroy
  fails until the lock clears (§5). Do not force-clear locks from the scheduler.

### Privileges for destroy

- Base: **`VM.Allocate`** ("create/remove VM on a server") on the guest —
  required to remove the guest.
- Disk deletion touches storage. In practice removing *referenced* disks is
  covered by the guest-level `VM.Allocate`, but destroying volumes on a
  datastore is governed by **`Datastore.Allocate`** ("create/modify/remove a
  datastore and delete volumes"). If the token is scoped narrowly and
  destroy/`destroy-unreferenced-disks` returns a permission error on volume
  removal, grant `Datastore.Allocate` (or at least `Datastore.AllocateSpace`)
  on the relevant storage. Validate against the real server (§7 test).

---

## 5. Idempotency & edge cases (critical for the scheduler)

The scheduler fires against guests whose state it observed *earlier*; by the
time an action runs, reality may have changed. Every handler must be
**idempotent and defensive**. Golden rule: **read `status/current` immediately
before acting, and treat "already in the desired state" as success, not error.**

| Situation | Observed behavior | Scheduler handling |
|-----------|-------------------|--------------------|
| `shutdown` / `stop` on an **already-stopped** guest | Version-sensitive. `stop` on a stopped guest is generally a no-op that ends `OK`; `shutdown` on a stopped guest has historically failed with `VM/CT is not running` (task `exitstatus != OK`), though newer versions may return OK. **Do not rely on either.** | Pre-check `status/current`; if `stopped`, **skip** the call and record success. |
| `start` on an **already-running** guest | Task fails: `... is already running`. | Pre-check; if `running`, skip and record success. |
| `destroy` on a **running** guest (qemu) | Task fails (running/lock). | Stop first (poll-then-force §1), then delete. LXC may use `force=1` but prefer stop-first. |
| Guest **no longer exists** (already deleted, or wrong VMID) | `status/current`, power, and delete calls return an error. Typically **HTTP 500** with a message like `Configuration file 'nodes/{node}/{t}/{vmid}.conf' does not exist` (config-not-found), *not* a clean 404. go-proxmox surfaces this as an error; Proxcloud's `mapErr` should classify config-not-found as a terminal "gone" state. | Treat "config does not exist" as **success for stop/delete** (goal achieved) and as a **skip** for start. Do not retry. |
| Guest **locked** (`backup` / `migrate` / `snapshot` / `clone` / `rollback`) | Power/destroy tasks fail immediately with `can't lock file ... got timeout` or `VM is locked (<reason>)`. | Read the `lock` field from `status/current` first; if locked, **defer/reschedule** the job (backoff + retry) rather than forcing. Never send `skiplock` (root-only, and unsafe) or clear the lock from the scheduler. For the graceful→force transition, use `stop` with **`overrule-shutdown=1`** to abort our *own* in-flight shutdown task (that lock is expected and ours to clear). |
| A graceful `shutdown` task is **still running** when the grace period expires | The guest holds a lock owned by the shutdown task. | Issue `stop` with `overrule-shutdown=1` to abort the shutdown task and hard-stop. |
| **HTTP 595** | `595` is a pveproxy/pvedaemon **transport/connectivity** status ("No route to host" / connection failure to the target node), not a guest-state error. Also relevant here: the Proxcloud server sits behind a Cloudflare Tunnel where **530** = tunnel down. | Treat 595 (and 5xx transport/530) as **retryable infrastructure errors**: do not mark the job failed-terminal; retry with backoff. Distinguish from a task that ran and returned a non-OK `exitstatus` (that is terminal). |
| Mutation call **times out** client-side but the task was created | The POST/DELETE may time out (mutation context) even though PVE queued the task. | If a UPID was returned, poll it. If no UPID, re-read `status/current` before retrying so you don't double-issue. Idempotent pre-checks make a duplicate fire harmless. |

**Design implication:** each scheduler handler should be shaped as
`observe → decide → (maybe) act → verify`, where "decide" short-circuits to
success whenever the guest is already in the target state, and "verify" gates on
`status/current`, not merely on `exitstatus: OK`.

---

## 6. Required token privileges (least privilege)

Per-action, on the guest path (`/vms/{vmid}` in ACL terms; can be granted via a
pool or per-VM ACL):

| Action | Privilege(s) |
|--------|--------------|
| Read `status/current`, task status, task log | `VM.Audit` |
| `start` / `shutdown` / `stop` / `reboot` / `reset` | `VM.PowerMgmt` |
| `DELETE` (destroy) | `VM.Allocate` (+ `Datastore.Allocate` on the storage if volume deletion is denied — see §4) |

Descriptions (from PVE "Permission Management"):
- **`VM.PowerMgmt`** — "power management (start, stop, reset, shutdown, …)".
- **`VM.Allocate`** — "create/remove VM on a server".
- **`VM.Audit`** — "view VM config".
- **`Datastore.Allocate`** — "create/modify/remove a datastore and delete volumes".
- **`Datastore.AllocateSpace`** — "allocate space on a datastore".

### `pveum` setup — minimal scheduler role

```bash
# A role that can observe, power-cycle, and destroy guests (no create/config).
pveum role add ProxcloudScheduler -privs "VM.Audit VM.PowerMgmt VM.Allocate"

# Grant it to the Proxcloud API token, scoped to a pool (preferred) ...
pveum acl modify /pool/proxcloud -token 'proxcloud@pam!scheduler' -role ProxcloudScheduler

# ... or scoped to specific guests / all VMs:
pveum acl modify /vms -token 'proxcloud@pam!scheduler' -role ProxcloudScheduler

# If destroy is denied on volume removal, add datastore rights on the storage:
pveum role add ProxcloudStorage -privs "Datastore.Allocate Datastore.AllocateSpace"
pveum acl modify /storage/<storeid> -token 'proxcloud@pam!scheduler' -role ProxcloudStorage
```

> Tokens by default have **"Privilege Separation"** on, meaning the token gets
> *only* the ACLs granted to the token itself (not the owning user's). Make sure
> the ACL is applied to the **token**, not just the user, or grant with
> `--propagate 1` on a pool. If the scheduler only ever stops/starts (never
> destroys), drop `VM.Allocate` and use just `VM.Audit VM.PowerMgmt`.

---

## 7. Open items to validate against the real server (9.2.4)

Two behaviors are version-sensitive and not authoritatively documented; verify
before shipping the scheduler and pin the results here:

1. **`shutdown` on an already-stopped guest** — does 9.2.4 return
   `exitstatus: OK` or fail with `... is not running`?
   Safe test on a throwaway guest:
   ```bash
   # stop it, confirm stopped, then re-issue shutdown and read task exitstatus
   curl -sk -H "$AUTH" -X POST "$PVE/nodes/$N/qemu/$V/status/stop"
   curl -sk -H "$AUTH" "$PVE/nodes/$N/qemu/$V/status/current" | jq .data.status
   UPID=$(curl -sk -H "$AUTH" -X POST "$PVE/nodes/$N/qemu/$V/status/shutdown" | jq -r .data)
   curl -sk -H "$AUTH" "$PVE/nodes/$N/tasks/$UPID/status" | jq '{status,exitstatus}'
   ```
   (Regardless of the answer, the scheduler pre-checks state and skips — this
   only confirms the fallback path.)
2. **`destroy` volume-permission scope** — with a token holding only
   `VM.Allocate` (no `Datastore.*`), does `DELETE ...?purge=1&destroy-unreferenced-disks=1`
   succeed on a guest with disks? If it returns a permission error, the storage
   ACL from §6 is required.

---

## What this means for the scheduler

Concrete recommended call sequences. Every sequence starts with an idempotent
state read and gates success on `status/current`, not on `exitstatus` alone.
All power/delete calls need `timeout`/`forceStop`/`overrule-shutdown` params, so
use **raw `/api2/json`** (existing `GuestAction` sends an empty body).

### (a) Scheduled auto-shutdown with grace (default 120s)

1. `GET status/current`. If `status=="stopped"` → **done** (success, no-op).
   If `lock` is set (not ours) → **reschedule** with backoff.
2. `POST status/shutdown` with `timeout=<grace+5>` (e.g. 125), `forceStop=0`.
   Keep the UPID.
3. Poll `GET status/current` every ~3s until `status=="stopped"` **or** grace
   (120s) elapses. Also watch the shutdown task's `exitstatus` for hard errors.
4. If still `running` at grace expiry → `POST status/stop` with
   `overrule-shutdown=1`. Poll `status/current` until `stopped`.
5. Success ⇔ `status=="stopped"`. Record which phase stopped it (graceful vs
   forced) in the activity log/audit.

### (b) Auto-start

1. `GET status/current`. If `status=="running"` → **done** (no-op). If `lock`
   set → reschedule.
2. `POST status/start` (qemu may pass `timeout`; lxc none). Keep UPID.
3. Poll task status to `exitstatus`; confirm with `GET status/current ==
   "running"`. If task fails with "already running" → treat as success.

### (c) TTL stop

Same as (a) if the policy is "graceful with grace". If the TTL policy is
"stop hard immediately" (no grace), skip straight to `POST status/stop`
(optionally `overrule-shutdown=1` if a shutdown might be in flight), then verify
`status/current=="stopped"`. Already-stopped → no-op success.

### (d) TTL delete

1. Ensure stopped: run sequence (a)/(c) first. **A VM must be stopped before
   `DELETE`**; an LXC could use `force=1` but prefer stop-first for clean phases.
2. `DELETE /nodes/{node}/{t}/{vmid}?purge=1&destroy-unreferenced-disks=1`
   (matches existing `DeleteGuest(purge=true)`). Keep UPID.
3. Poll task status → success ⇔ `exitstatus=="OK"`.
4. If a later `status/current` returns "config does not exist" → the guest is
   gone → **success** (idempotent: a re-fired delete on an absent guest is a
   no-op success, not an error).
5. On lock error → reschedule with backoff; never `skiplock`.
6. On 595/530/transport error → retryable, not terminal.
