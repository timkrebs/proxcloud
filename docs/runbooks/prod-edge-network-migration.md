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

**What happens until you do this:** nothing breaks. Once the new files are on
the guest, `bootstrap.sh` finishes everything else and exits 3, and every
`deploy.sh <ref>` stops with "edge network needs its one-time migration" before
touching any container — prod keeps serving the current release. `deploy.sh
--rollback` still works (it never touches the network).

## Facts

| Thing | Value |
|---|---|
| Edge subnet / gateway | `10.254.254.0/24` / `10.254.254.1` |
| Dynamic address range | `10.254.254.128/25` (Caddy's pin can never be taken) |
| Caddy's pinned address | `10.254.254.10` |
| Caddy trusts `CF-Connecting-IP` from | `10.254.254.1/32` (the gateway = host cloudflared) |
| Backend `TRUSTED_PROXY_CIDRS` | `10.254.254.10/32` (Caddy only) |
| Caddy published port | `127.0.0.1:80` only (tunnel-only; no LAN, no 443) |
| Downtime | ~1 minute of edge (Postgres and the color containers keep running) |

---

## Step 0 — [prod] Pre-checks (no impact)

1. **Point cloudflared at the loopback origin.** The tunnel's ingress service
   must be `http://127.0.0.1:80` — not the guest's LAN IP (the origin will
   stop listening there) and not `localhost` (it may resolve to `::1`, which is
   not published). Check `/etc/cloudflared/config.yml`, or for a
   dashboard-managed tunnel: Zero Trust → Networks → Tunnels → your tunnel →
   Public hostnames. This change works with the old binding too, so make it
   now and confirm the site still loads.
2. **Make sure the subnet is free** on this guest — both must print nothing:

   ```bash
   ip route | grep '10\.254\.254\.'
   docker network ls -q | xargs docker network inspect \
     -f '{{.Name}} {{range .IPAM.Config}}{{.Subnet}} {{end}}' | grep '10\.254\.254\.'
   ```

   If either prints something, stop: the addressing in
   `deploy/host/prod/bin/ensure-networks.sh`, `caddy/docker-compose.yml`,
   `caddy/Caddyfile` and `env.example` must move to another subnet first.

## Step 1 — [prod] Put the new files in place

Provision `deploy/host/common/bin/*` + `deploy/host/prod/*` onto the guest the
usual way (the prod Terraform provisioner, or copy them to `/opt/proxcloud`).
`bootstrap.sh` then reports:

```text
prod bootstrap: complete EXCEPT the edge network — migrate it (...)
```

That exit 3 is expected here.

## Step 2 — [prod] Set the backend's trusted proxy

In `/opt/proxcloud/.env`:

```ini
TRUSTED_PROXY_CIDRS=10.254.254.10/32
```

The running backend picks it up at its next deploy (Step 5).

## Step 3 — [prod] Recreate the edge network (maintenance window)

```bash
cd /opt/proxcloud
docker compose --env-file .env -p proxcloud-caddy -f caddy/docker-compose.yml down
members="$(docker network inspect proxcloud-edge -f '{{range .Containers}}{{.Name}} {{end}}')"
for c in $members; do docker network disconnect proxcloud-edge "$c"; done
docker network rm proxcloud-edge
bin/ensure-networks.sh
for c in $members; do docker network connect proxcloud-edge "$c"; done
bin/up-infra.sh
```

The color containers are only detached and re-attached, never stopped; Caddy
finds them by container name, so they need no special aliases.

## Step 4 — [prod] Verify

```bash
docker network inspect proxcloud-edge \
  -f '{{range .IPAM.Config}}{{.Subnet}} {{.IPRange}} {{.Gateway}}{{end}}'
#   -> 10.254.254.0/24 10.254.254.128/25 10.254.254.1
docker inspect proxcloud-caddy \
  -f '{{(index .NetworkSettings.Networks "proxcloud-edge").IPAddress}}'
#   -> 10.254.254.10
ss -ltn | grep ':80 '          # -> only 127.0.0.1:80
```

Then: the public URL loads through the tunnel, and from another machine on the
LAN `curl http://<prod-guest-ip>/` is refused.

## Step 5 — [runner] Deploy, then check real client IPs arrive

Run the next prod deploy (or redeploy the current ref). It recreates the backend
with the new `TRUSTED_PROXY_CIDRS`. Then sign in again through the public URL
(a session records its IP when it is created, so older sessions still show the
old value) and open **Settings → Sessions**: the new session's IP must be your
real public address. If it shows `10.254.254.1` or `10.254.254.10`, the trust
chain is not matching — recheck Steps 0.1 and 2 and the Caddyfile's
`trusted_proxies` line.

## Rollback

Restore the previous release's `caddy/docker-compose.yml`, `caddy/Caddyfile`,
`caddy/upstream/*.caddy`, `bin/deploy.sh` and `bin/up-infra.sh` on the guest,
remove the `TRUSTED_PROXY_CIDRS` line from `.env`, then repeat Step 3 with
`docker network create proxcloud-edge` in place of `bin/ensure-networks.sh`.
If cloudflared cannot reach `127.0.0.1:80`, the old binding also listened there,
so Step 0.1 needs no rollback.
