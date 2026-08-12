# ADR-0015: Blue/green reverse proxy — Caddy for the prod atomic switch

Date: 2026-08-12 · Status: accepted (proxy = Caddy) · Delivery/CD

## Context

Prod runs on one VM with a **blue/green** layout: two compose projects
(`proxcloud-blue`, `proxcloud-green`), exactly one live, behind a reverse proxy;
Postgres is a **single shared** instance outside the color pair (ADR-0014,
release-engineer.md). The proxy must: (a) switch upstreams **atomically and
zero-drop** on cutover and switch back on rollback; (b) terminate TLS for
`staging.<domain>` and the prod URL; (c) pass **SSE** (`/api/events`) and the
**console WebSocket** (`/api/console/ws/*`) through with **buffering off** and
correct `Upgrade`/`Connection` handling; (d) let the deploy script health-check the
**idle** color **directly, bypassing the proxy**, before the switch. One engineer
maintains this. Choose the proxy and pin the concrete switch mechanism.

## Decision

### 1. Proxy = **Caddy** (not Traefik)
For a single-node, single-maintainer homelab Caddy wins on the axes that matter:
- **Config-reload simplicity / zero-drop switch.** `caddy reload` swaps the running
  config gracefully, draining in-flight connections — exactly the primitive a
  cutover needs. The whole edge is **one Caddyfile**; the switch is a file swap +
  reload, trivially scriptable and reviewable.
- **Automatic TLS** for `staging.<domain>` and the prod URL out of the box (ACME),
  or internal-CA / plain-HTTP-behind-cloudflared with a one-line change.
- **Transparent SSE + WebSocket.** Caddy's `reverse_proxy` forwards `Upgrade`/
  `Connection` natively (WS just works) and disables response buffering per-route
  with `flush_interval -1` (required for SSE). No provider/label indirection.
- **Fewer moving parts.** One static Caddyfile beats Traefik's
  provider/label/dynamic-config model for a **single static upstream swap**; less to
  learn, less to break, easier to reason about at 2am.

### 2. On-VM layout — three compose projects on shared docker networks
```
/opt/proxcloud/
  data/       # compose: proxcloud-data  → postgres + volume; net proxcloud-data-net (external)
  blue/       # compose: proxcloud-blue  → backend+frontend; nets proxcloud-edge, proxcloud-data-net
  green/      # compose: proxcloud-green → backend+frontend; nets proxcloud-edge, proxcloud-data-net
  caddy/      # Caddyfile + upstream/{blue.caddy,green.caddy,active.caddy->}
  .env        # ALL app secrets (manual, never in git/CI)
  bin/deploy.sh
  state/      # live-color, last-cutover marker (ADR-0014 §5)
```
- Two **external** docker networks: `proxcloud-edge` (Caddy + both colors) and
  `proxcloud-data-net` (both colors + Postgres). Caddy resolves color containers by
  name (`proxcloud-blue-backend`, `proxcloud-green-frontend`) over `proxcloud-edge`.
- **Postgres lives in `proxcloud-data` only** — it does **not** blue/green; both
  colors point `DATABASE_URL` at `proxcloud-data-postgres:5432`. Schema
  compatibility across colors is guaranteed by expand/contract migration discipline
  (ADR-0014 §4, release-engineer.md), not by duplicating data.
- Each color **also** publishes its backend/frontend on distinct **loopback** ports
  (blue `127.0.0.1:18080/13000`, green `127.0.0.1:28080/23000`) used only for
  pre-switch health checks (§4).

### 3. Switch mechanism — imported snippet behind an atomically-renamed symlink
The Caddyfile imports one file:
```
proxcloud.<domain> {
    import /opt/proxcloud/caddy/upstream/active.caddy
}
```
`active.caddy` is a **symlink** → `blue.caddy` **or** `green.caddy`. Each color
snippet defines the routing to that color:
```
# green.caddy
@api path /api/*
handle @api {
    reverse_proxy proxcloud-green-backend:8080 {
        flush_interval -1                 # SSE: no buffering
        transport http { versions 1.1 }  # WS: keep h1 for Upgrade
    }
}
handle {
    reverse_proxy proxcloud-green-frontend:3000
}
```
Cutover (in `deploy.sh`): `ln -sfn green.caddy .../active.caddy` (atomic
`rename(2)`) → `caddy reload --config /opt/proxcloud/caddy/Caddyfile`. Reload is
graceful/zero-drop; in-flight SSE/WS on the old color drain naturally (old color
stays warm). **Rollback = flip the symlink back + reload** — the ADR-0014 auto-
rollback path. The current color is recorded in `state/live-color`; `deploy.sh`
reads it to pick the idle target and rewrites it only **after** a successful reload.

### 4. Health-check the idle color bypassing the proxy
Before flipping the symlink, `deploy.sh` curls the idle color's **loopback** ports
directly — `http://127.0.0.1:28080/api/health` and `/api/v1/version` (assert ==
deployed SHA) — so the new stack is proven **independently of Caddy**. Only on pass
does the atomic switch happen. This is the mechanism ADR-0016's prod smoke assumes.

### 5. SSE / WebSocket passthrough specifics
- **SSE** (`/api/events`): `flush_interval -1` on the `/api/*` route disables
  response buffering so events flush immediately; site-level `timeouts` are set long
  enough for long-lived streams (no idle read timeout that would kill a quiet feed).
- **Console WS** (`/api/console/ws/*`): Caddy upgrades natively; pin
  `transport http { versions 1.1 }` so the hop stays HTTP/1.1 and the `Upgrade`
  handshake is not coalesced into h2. The browser connects to the backend WS origin;
  Caddy forwards the handshake and `X-Forwarded-*` untouched.

### 6. TLS strategy — one flag for Tim
Default: **Caddy terminates TLS via ACME** for `staging.<domain>` and the prod URL.
If the prod guest is fronted by **Cloudflare Tunnel** (per server facts, `pve01`
already sits behind one), TLS terminates at Cloudflare and Caddy serves plain HTTP
to `cloudflared`; that is a one-line site-address change. Which path prod uses
**needs Tim** (depends on the lab domain and whether the tunnel fronts the prod
guest). Staging defaults to Caddy-ACME on `staging.<domain>`. Domain values stay
`<domain>` placeholders until provided — not a blocker for building the topology.

## Consequences

- Cutover and rollback are both "swap a symlink + `caddy reload`": a couple of
  seconds, zero-drop, trivially auditable, and identical in both directions.
- The whole edge is one Caddyfile plus two tiny per-color snippets — minimal
  surface for a solo maintainer; SSE/WS correctness is two directives, not a config
  subsystem.
- Postgres as a third, shared compose project means migrations are the only cross-
  color coupling, which is exactly what the expand/contract discipline governs.
- Idle-color health checks run on loopback ports, so the proxy is never in the path
  of the go/no-go decision.
- **Needs Tim / coordinate:** lab domain(s); whether prod is behind Cloudflare
  Tunnel (sets TLS mode); the shared external networks and loopback port map must be
  created by the Terraform/bootstrap that provisions the guests (deploy-owner task).

## Alternatives considered

- **Traefik** — capable of the same passthrough and dynamic config, but its
  label/provider model is heavier than a single static upstream swap needs; for one
  node and one maintainer it adds concepts (routers/services/providers) without
  buying anything Caddy's file-swap-and-reload doesn't already give. Rejected on
  maintainability, not capability.
- **Two colors + a shared Postgres inside one compose project** — rejected: couples
  data lifecycle to a color and makes "keep old color warm" fragile; Postgres as its
  own project is the clean seam.
- **HAProxy/nginx with a hot-reconfig** — rejected: more config ceremony and manual
  ACME; Caddy folds TLS, reload, and Upgrade handling into defaults.
- **DNS or IP failover for the switch** — rejected: TTL/propagation makes it non-
  atomic and non-instant-rollback; an in-proxy upstream swap is immediate.
- **Draining via container restart instead of `reload`** — rejected: drops in-flight
  SSE/WS; `caddy reload` preserves them, which the console and live metrics need.
