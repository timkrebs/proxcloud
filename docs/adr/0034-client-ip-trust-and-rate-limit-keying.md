# ADR-0034: Client-IP trust chain & rate-limit keying

Date: 2026-09-24 · Status: accepted · Security-critical

> **Revised before merge (2026-09-24, 2026-09-25).** The first version of this
> decision failed its security re-review: the prod Caddy trust setting
> (loopback) could never match the real topology, any cookie value earned its
> own rate-limit bucket, `X-Forwarded-For` was still a fallback, a lockout-map
> flood could evict a victim's lock, and failure streaks decayed as fast as
> locks expired. A third round then found that locked junk could still crowd a
> fresh account out of the lockout map, that one account could multiply its
> throttle budget by signing in repeatedly, and that the edge migration could
> route prod to the wrong color. This text records the corrected decision; the
> findings are in `docs/security/audit-2026-08-14.md` (H2, H3, second and
> third review rounds).

## Context

The production topology is `browser → Cloudflare → cloudflared (a service on
the prod host) → Caddy (container) → backend (container)`. Two facts break
every naïve per-IP control there:

1. **Every user shares one TCP peer.** cloudflared reaches Caddy through
   Caddy's published port, so Docker relays every tunneled connection into the
   container from the edge network's gateway; the backend in turn sees only
   Caddy. A per-IP limit keyed on the connection puts ALL honest users into ONE
   bucket: ~10 rps of unauthenticated junk 429s the whole portal, and five junk
   login attempts a minute lock login for everyone.
2. **Forwarded headers are client-controlled unless an edge owns them.** Caddy
   manages `X-Forwarded-For`/`-Proto`/`-Host` itself but passes a
   CLIENT-SUPPLIED `X-Real-IP` through untouched unless explicitly set, and the
   first entry of `X-Forwarded-For` is whatever the client sent. A backend that
   trusts either from its proxy lets any client choose its identity per request
   — defeating per-IP login limiting, forging the `audit_log`/`sessions` IP
   columns, and minting unbounded limiter keys.

A third gap compounded the second: a CORRECT password reset both the per-IP
login window and the per-account lockout, so a known-password attacker could
mint fresh TOTP challenges indefinitely and grind the second factor.

## Decision

### 1. Trusted-proxy chain contract (pinned addresses, edge overwrites, backend validates)

- **Addresses the trust decisions name are pinned.** The prod
  `proxcloud-edge` network is created only by `bin/ensure-networks.sh` with
  subnet `10.254.254.0/24`, gateway `10.254.254.1` and dynamic range
  `10.254.254.128/25`; Caddy is pinned at `10.254.254.10`, outside that range so
  no redeployed container can take it. An existing network with other
  addressing is never modified in place: deploys refuse before touching a
  container until the one-time migration (`bin/migrate-edge-network.sh`, per
  `docs/runbooks/prod-edge-network-migration.md`) is done. Provisioning never
  changes the live color: the `active.caddy` link is guest state that
  `bootstrap.sh` points at `state/live-color`.
- **The origin is tunnel-only.** Caddy publishes `127.0.0.1:80` only, so the
  only connections arriving from the gateway are from processes on the prod host
  — cloudflared.
- **Caddy derives the client IP at the edge.** The prod Caddyfile's global
  `servers` block sets `trusted_proxies static 10.254.254.1/32` and
  `client_ip_headers CF-Connecting-IP`: `{client_ip}` is Cloudflare's reported
  client only for connections from the gateway; from any other peer it falls
  back to the TCP peer (fail-safe: a shared bucket, nothing spoofable).
- **Caddy ALWAYS overwrites `X-Real-IP`.** Every environment sets
  `header_up X-Real-IP {client_ip}` on the backend route AND the frontend route
  (so nothing a frontend forwards can carry a client value), and the API route
  matches bare `/api` as well as `/api/*`. QA and staging front clients
  directly, where `{client_ip}` is simply the peer.
- **The backend trusts exactly Caddy, then verifies.** `trustedProxyHeaders`
  reads the client IP ONLY from `X-Real-IP`, only when `TRUST_PROXY_HEADERS` is
  on and the immediate peer is inside `TRUSTED_PROXY_CIDRS` (prod:
  `10.254.254.10/32` — Caddy alone, not the edge subnet with both colors'
  frontends), and only when it parses via `net.ParseIP`. `X-Forwarded-For` is
  never consulted. Anything else keeps the direct peer; a non-IP string is never
  written into RemoteAddr. From an untrusted peer the forwarded headers are
  stripped outright.

### 2. Global rate-limit keying: signed-in user, else client IP

The global 600/min throttle keys a request by its signed-in USER once
`Authenticate` has accepted its session cookie, and by its client IP
otherwise. The limiter wraps `Authenticate` (so the two cannot be reordered)
and learns from its verdict: an accepted cookie is marked — a seeded
`hash/maphash` of the value, mapped to the context identity's user id, in a
bounded set of 8,192 — and a cookie it turns away (revoked, expired, never
valid) loses its mark at once. A mark also lapses 15 minutes after the cookie
was last accepted, so a cookie revoked where the limiter cannot see it
(another device, an admin) stops spending its former user's budget even if it
is only ever sent to public routes; the UI polls every few seconds, so open
tabs never lapse. Every unmarked request — cookie-less, or carrying a cookie
the server has not validated — draws from its client IP's bucket, IPv6 keyed
by /64. A made-up cookie therefore buys nothing, all of one account's sessions
share one budget, and no request ever costs a DB lookup in the limiter.

Buckets are O(1) fixed-window counters in a map hard-capped at 16,384 keys;
expired buckets are swept at most every 10 s, and a full map evicts an
arbitrary bucket in O(1). That bounds memory and per-request cost under any
flood, but not per-key fairness: once more than 16,384 keys are live, repeated
eviction keeps resetting throttled buckets, so a flood spread over that many
keys (distinct /64s, say) is no longer held to 600/min per key — the third
review drove 1.6M requests through a map pinned at 16,384. The downstream caps
(Proxmox concurrency limit, DB pool, request timeouts) still bound what such a
flood can do. Only `/api/health` and `/api/v1/version` are exempt; the
streaming routes count once at open and stay exempt from the body cap and
request timeout. A session's first request after sign-in still counts against
its IP's bucket.

### 3. Per-account second-factor counter

Each TOTP/recovery attempt is **reserved atomically with the lock check** —
counted as a failure up front, refunded by a valid code — so concurrent
attempts cannot slip past a check another is about to invalidate. The counter
is keyed per account (user id) and a **correct password never resets it**. Ten
failures lock the 2FA STEP (not the account) for 15 minutes. The failure that
engages the lock is marked `second_factor_locked` in its audit row, and the
first refused attempt of each lock writes one audit row — reaching the second
factor at all means someone holds the password.

### 4. Bounded, decaying login lockout

The per-account password lockout keys on **SHA-256 of the lowercased/trimmed
email** alone — whether or not an account exists — so every email locks,
escalates and survives floods alike, and a lockout reveals nothing about which
emails are registered (emails over RFC 5321's 254 bytes are rejected before the
limiter is touched). Locks escalate but **cap at 15 minutes**. A failure streak
resets only after **2 hours without a failure** — far longer than the longest
lock — so once an account is driven into lockout, each lock's expiry buys an
attacker exactly one guess, not a fresh set. Memory is bounded in two tiers:

- Keys below the threshold live in a streak map **hard-capped at 10k**. When
  it is full, the unprotected entry whose relevance ends soonest makes room,
  so junk can only push out a streak of fewer than five failures, and only
  after cycling out every older streak: about four guesses per ~10k junk
  password hashes.
- A key reaching the threshold moves to a **lock map that nothing is evicted
  from**, capped at 2^18. Each entry there costs an attacker five failed
  password hashes, and the 4-slot hash semaphore bounds those globally: at the
  ~70 verifications/s the third review measured, at most ~100k keys can be at
  the threshold within one decay window.
- Were both ever full, an email not already tracked is **refused** (fail
  closed, logged as saturation) — never allowed with its failures uncounted.

Per-IP login windows key IPv6 by /64. The check-before-password ordering is
kept (no lockout-oracle timing).

## Consequences

- Spoofing the client identity requires code execution on the prod host, in
  Caddy, or — to ARP-spoof the gateway or Caddy's address — in a container on
  `proxcloud-edge` that holds NET_RAW, which the prod Caddy and color
  containers drop. A mis-set `TRUSTED_PROXY_CIDRS` or `trusted_proxies` only
  over-counts (everyone as the proxy) — it never re-opens spoofing.
- Honest users behind the tunnel are isolated per signed-in user; probes
  (HEALTHCHECK, deploy gates) can never be starved by a flood; random cookies,
  extra sessions and address rotation within one /64 mint no extra budget. A
  subscriber holding a larger IPv6 prefix (commonly a /56 to a /48) still gets
  one bucket per /64 it uses.
- TOTP brute force is bounded at ~10 guesses per account per 15 minutes
  regardless of password knowledge, concurrency, source-IP rotation, or
  challenge minting, and every lock is visible in the audit trail.
- The edge migration is an explicit, one-time operator step, done by
  `bin/migrate-edge-network.sh`: every pre-check runs before anything changes,
  and a failure prints the edge's state with the commands to finish or abort.
  Until it is done, prod keeps serving the current release and deploys refuse.
- Regression tests pin the backend half of the contract
  (`TestForwardedIPValidation`, `TestTrustedProxyHeaders`,
  `TestRandomCookiesShareIPBucket`, `TestRouterMarksOnlyAuthenticatedSessions`,
  `TestSessionsOfOneUserShareABucket`, `TestSessionMarksFollowAuthenticate`,
  `TestSessionMarkExpires`, `TestRouterForgetsRevokedSession`,
  `TestLimiterStaysBounded`, `TestLockEvictionNeverUnlocks`,
  `TestJunkFloodCannotUntrackAccounts`, `TestFullAccountTrackerFailsClosed`,
  `TestLoginLockoutSurvivesJunkFlood`, `TestSecondFactorReserveIsAtomic`,
  `TestSecondFactorLockoutAcrossChallenges`); the Caddy half is configuration
  validated with `caddy validate`.

### Accepted residual: in-memory, per-instance lockout state

All limiter/lockout state (IP windows, account lockout, 2FA counter, global
throttle, validated sessions) is **in-memory per backend instance**.
Blue/green runs one active color, so in practice one instance enforces the
bounds; a future multi-replica deployment would multiply every budget by the
replica count and lose state on restart. A DB-backed lockout is an explicit
open item in the findings register — the per-challenge attempt counter
(`login_challenges.attempts`) is already DB-backed and caps a single challenge
at 5 attempts even across instances.

### Accepted residual: validated-session churn

Nothing caps how many sessions one account holds, and every accepted cookie
takes a mark in the 8,192-entry set. An account that signs in over and over
(each sign-in costs a password hash and is rate-limited per IP) can push other
users' marks out, sending them back to their IP's bucket until their next
authenticated request marks them again — a nuisance only in combination with a
flood from the same IP. A per-user session cap is tracked in the register.

## Alternatives considered

- **Trust `X-Forwarded-For` and walk the chain.** XFF is append-semantics: the
  first hop is client-controlled unless every proxy in the chain is enumerated
  and stripped correctly; Cloudflare→cloudflared→Caddy makes that a three-party
  parsing contract. A single, edge-overwritten `X-Real-IP` is one assignment
  with one owner. Rejected for fragility.
- **Verify `CF-Connecting-IP` at the backend.** Couples the backend to
  Cloudflare and still requires trusting the intermediate hops not to inject
  it; the edge (Caddy) already knows the authentic peer. Rejected.
- **Trust the whole edge subnet in the backend.** Simpler to configure, but
  the subnet also holds both colors' frontends, so anything a frontend
  forwarded would be trusted. Rejected in favour of Caddy's pinned /32.
- **Key the limiter on any session-cookie value.** Needs no auth coupling, but
  every made-up value becomes its own bucket — the first version of this
  decision, which let one IP send 60,000 unthrottled requests and grew the map
  without bound. Rejected.
- **Look the session up in the limiter.** Correct, but puts a DB query in front
  of every request including floods — the amplification the limiter exists to
  prevent. The validated-session set gets the same answer with no lookup.
  Rejected.
- **A bucket per validated session.** The second version of this decision:
  every sign-in minted a fresh 600/min budget, so one account could multiply
  its budget by signing in repeatedly. Rejected for per-user buckets.
- **Separate lockout maps for existing accounts and unknown emails.** Junk
  could then never crowd a real account out, but the two maps fill
  differently under a flood: locks on unknown emails would be dropped while
  real accounts' locks survive, telling a flooder which emails exist. Rejected
  during the third round's remediation, before merge.
- **A count-min sketch for overflow lockout keys.** It can only over-count,
  so saturation could never suppress a lock; but keeping false locks
  negligible at the hash rate takes megabytes, and overflow keys would lock on
  a different schedule. The two-tier map gives the same guarantee within the
  hash-rate bound more simply. Not pursued.
- **Containerize cloudflared on the edge network.** Would give it a pinned
  address of its own instead of arriving via the gateway; equally sound, but
  cloudflared already runs as a host service. Left as an option (noted in the
  Caddyfile).
- **DB-backed lockout now.** Deferred (accepted residual above): correct
  multi-instance semantics need care (atomic upsert windows, decay in SQL) and
  the current deployment is single-active-instance.
