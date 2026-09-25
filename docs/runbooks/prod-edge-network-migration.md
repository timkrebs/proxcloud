# Runbook — Migrate the prod edge network to pinned addressing (one-time)

Scope: the **prod** guest only, once. It moves `proxcloud-edge` onto the pinned
addressing that the client-IP trust chain depends on (ADR-0034), and makes the
origin tunnel-only. QA and staging are not affected.

**Why:** cloudflared runs on the prod guest and reaches Caddy through its
published port, so every tunneled request arrives at Caddy from the edge
network's gateway. Caddy may only take the real client IP from
`CF-Connecting-IP` for that one source, and the backend may only read
`X-Real-IP` from Caddy's own address. On the old, Docker-assigned network
neither address was fixed, so no trust setting could match — every anonymous
user shared one rate-limit bucket (security review H-C).

**What happens until you do this:** nothing breaks. Once this release's files
are on the guest, `bootstrap.sh` finishes everything else and exits 3, and every
`deploy.sh <ref>` stops with "edge network needs its one-time migration" before
touching any container — prod keeps serving the current release.
`deploy.sh --rollback` still works (it never touches the network). Provisioning
never changes which color is live: `caddy/upstream/active.caddy` is no longer
shipped, and `bootstrap.sh` points it at `state/live-color`.

The migration is done by `bin/migrate-edge-network.sh`. It runs every
pre-check before it changes anything, and if a step fails it stops there and
prints the state of the edge with the exact commands to finish or to abort.

## Facts

| Thing | Value |
|---|---|
| Edge subnet / gateway | `10.254.254.0/24` / `10.254.254.1` |
| Dynamic address range | `10.254.254.128/25` (Caddy's pin can never be taken) |
| Caddy's pinned address | `10.254.254.10` |
| Caddy trusts `CF-Connecting-IP` from | `10.254.254.1/32` (the gateway = host cloudflared) |
| Backend `TRUSTED_PROXY_CIDRS` | `10.254.254.10/32` (Caddy only) |
| Caddy published port | `127.0.0.1:80` only (tunnel-only; no LAN, no 443) |
| Migration script | `/opt/proxcloud/bin/migrate-edge-network.sh [--check]` (root) |
| Saved pre-migration Caddy files | `/root/proxcloud-caddy.pre-edge-migration` |
| Downtime | a few seconds of edge, then a short `/api` blip while the live backend restarts; Postgres and the color containers keep running |

---

## Step 0 — [prod] Before you start (no impact)

1. **Point cloudflared at the loopback origin.** The tunnel's ingress service
   must be `http://127.0.0.1:80` — not the guest's LAN IP (the origin will
   stop listening there) and not `localhost` (it may resolve to `::1`, which is
   not published). Check `/etc/cloudflared/config.yml`, or for a
   dashboard-managed tunnel: Zero Trust → Networks → Tunnels → your tunnel →
   Public hostnames. This works with the old binding too, so change it now and
   confirm the site still loads.
2. **Docker Engine 28 or newer** (`docker version --format '{{.Server.Version}}'`).
   Older engines let LAN hosts reach ports published on `127.0.0.1`
   (moby#48721), which would defeat tunnel-only. If you cannot upgrade, add the
   equivalent rule and persist it across reboots:

   ```bash
   iptables -t raw -I PREROUTING ! -i lo -d 127.0.0.0/8 -j DROP
   ```

   The script refuses to run on an older engine without that rule.

## Step 1 — [prod] Save the current Caddy files, then provision

Save the running Caddy config first — it is the abort path, so it must be the
copy from **before** provisioning:

```bash
sudo cp -a /opt/proxcloud/caddy /root/proxcloud-caddy.pre-edge-migration
```

Then provision `deploy/host/common/bin/*` + `deploy/host/prod/*` onto the guest
the usual way (the prod Terraform provisioner, or copy them to
`/opt/proxcloud`). `bootstrap.sh` then reports:

```text
prod bootstrap: complete EXCEPT the edge network — migrate it (...)
```

That exit 3 is expected here.

## Step 2 — [prod] Set the backend's trusted proxy

In `/opt/proxcloud/.env`:

```ini
TRUSTED_PROXY_CIDRS=10.254.254.10/32
```

## Step 3 — [prod] Dry run

```bash
sudo /opt/proxcloud/bin/migrate-edge-network.sh --check
```

Every line must read `ok` (a `note` is information). It checks that:

- `state/live-color`, the `active.caddy` link and the running live containers
  agree;
- the new Caddy files are in place, the saved copy from Step 1 exists, and
  `.env` has the trust setting from Step 2;
- no host route or Docker network already uses `10.254.254.0/24` (it creates
  and removes a throwaway probe network to be sure);
- the new Caddyfile validates, and the Docker Engine version is safe (Step 0.2);
- the live color's image can boot against the current schema and `.env` — a
  dry run of its migrator, which applies nothing the live backend has not
  already applied. If this passes, the migration recreates the live backend so
  the trust setting applies at once. If not (for example after a rollback, the
  live image is older than the schema), the script says so and leaves the
  backend alone; Step 6 then applies the setting.

It lists the containers it will move, including stopped ones (the retired
color, stopped after its soak), which could otherwise never start again once
the old network is gone.

## Step 4 — [prod] Migrate (maintenance window)

```bash
sudo /opt/proxcloud/bin/migrate-edge-network.sh
```

It repeats the pre-checks, then:

1. stops Caddy (the edge is down from here);
2. detaches every container from the old `proxcloud-edge`, running or not;
3. removes the old network and creates it pinned (`bin/ensure-networks.sh`);
4. re-attaches the containers — Caddy finds them by container name;
5. starts the new Caddy (`bin/up-infra.sh`) and checks that it sits on
   `10.254.254.10` and answers `/api/health` — the edge is back;
6. if the dry run passed, recreates the live backend from its current image
   with the new `.env`, and waits until it is healthy directly and through
   Caddy.

**If it fails**, it stops at the failing command and prints the state — the
network (`old`, `pinned` or `missing`), which containers are still detached,
whether Caddy runs — with the exact commands to **finish** or to **abort**.
The edge stays down until you do one or the other. Aborting restores the
pre-migration edge from the Step 1 copy.

## Step 5 — [prod] Verify

```bash
docker network inspect proxcloud-edge \
  -f '{{range .IPAM.Config}}{{.Subnet}} {{.IPRange}} {{.Gateway}}{{end}}'
#   -> 10.254.254.0/24 10.254.254.128/25 10.254.254.1
docker inspect proxcloud-caddy \
  -f '{{(index .NetworkSettings.Networks "proxcloud-edge").IPAddress}}'
#   -> 10.254.254.10
ss -ltn | grep ':80 '          # -> only 127.0.0.1:80
```

Then:

- the public URL loads through the tunnel;
- from another machine on the LAN, `curl http://<prod-guest-ip>/` is refused;
- sign in again through the public URL (a session records its IP when it is
  created, so older sessions still show the old value) and open
  **Settings → Sessions**: the new session's IP must be your real public
  address. If it shows `10.254.254.1` or `10.254.254.10`, the trust chain is
  not matching — recheck Steps 0.1 and 2 and the Caddyfile's
  `trusted_proxies` line. (If the script did not recreate the live backend,
  this check has to wait for Step 6.)

## Step 6 — [runner] Deploy

Run the next prod deploy (or redeploy the current ref). It brings up the other
color with the new `.env` and cuts over to it. The color that was live keeps
running warm with whatever `.env` it last started with: if the script recreated
it, both colors now carry the trust setting; if not, run one more deploy so a
rollback can never land on a backend without it. Until a color has the
setting, the backend sees every request as coming from Caddy — every client
then shares one rate-limit bucket, but nothing becomes spoofable.

## Abort and rollback

**During the migration**, follow the commands the script prints. The same
steps by hand, from any state:

1. if `docker network inspect proxcloud-edge` fails, recreate it the old way:
   `docker network create proxcloud-edge`;
2. re-attach every color container that is not attached
   (`docker network connect proxcloud-edge <name>`; the four are
   `proxcloud-{blue,green}-{backend,frontend}`);
3. put the saved Caddy files back and start Caddy:

   ```bash
   cp /root/proxcloud-caddy.pre-edge-migration/{docker-compose.yml,Caddyfile} /opt/proxcloud/caddy/
   docker compose --env-file /opt/proxcloud/.env -p proxcloud-caddy \
     -f /opt/proxcloud/caddy/docker-compose.yml up -d
   ```

The old Caddy config also runs on the new, pinned network, so step 1 is only
needed if the network is gone.

**After a completed migration**, to go back to the previous release: restore
that release's `caddy/docker-compose.yml`, `caddy/Caddyfile`, `bin/deploy.sh`
and `bin/up-infra.sh` (the Step 1 copy has the Caddy files), set
`TRUSTED_PROXY_CIDRS` back to its previous value, and start Caddy as above. The
pinned network can stay — nothing in the previous release depends on the old
addressing. If cloudflared cannot reach `127.0.0.1:80`, the old binding also
listened there, so Step 0.1 needs no rollback.
