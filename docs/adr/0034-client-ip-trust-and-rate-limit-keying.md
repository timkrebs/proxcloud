# ADR-0034: Client-IP trust chain & rate-limit keying

Date: 2026-09-24 · Status: accepted · Security-critical

## Context

The production topology is `browser → Cloudflare → cloudflared → Caddy →
backend`. Two facts break every naïve per-IP control there:

1. **Every user shares one TCP peer.** All tunnel traffic reaches Caddy from
   cloudflared, and reaches the backend from Caddy — so the connection's
   RemoteAddr identifies the proxy, never the user. A per-IP request limit
   keyed on it puts ALL honest users into ONE bucket: ~10 rps of
   unauthenticated junk 429s the whole portal, and five junk login attempts a
   minute lock login for everyone.
2. **`X-Real-IP` was client-controlled.** Caddy manages `X-Forwarded-For`/
   `-Proto`/`-Host` itself but passes a CLIENT-SUPPLIED `X-Real-IP` through
   untouched unless explicitly set — and the backend PREFERRED `X-Real-IP` and
   copied it into RemoteAddr unvalidated whenever the peer was inside
   `TRUSTED_PROXY_CIDRS`. Consequence: any client could spoof its identity per
   request — defeating per-IP login limiting (TOTP brute force at Argon2
   speed), forging the `audit_log`/`sessions` IP columns, and growing limiter
   maps without bound with arbitrary attacker-chosen keys.

A third gap compounded the second: a CORRECT password reset both the per-IP
login window and the per-account lockout, so a known-password attacker could
mint fresh TOTP challenges indefinitely and grind the second factor.

## Decision

### 1. Trusted-proxy chain contract (edge overwrites, backend validates)

- **Caddy derives the client IP at the edge.** The prod Caddyfile's global
  `servers` block sets `trusted_proxies static <cloudflared source>` and
  `client_ip_headers CF-Connecting-IP`: `{client_ip}` is Cloudflare's reported
  client ONLY when the connection actually comes from cloudflared; from any
  other peer it falls back to the TCP peer (fail-safe: shared bucket, nothing
  spoofable). The cloudflared source is an operator setting (loopback for
  on-box cloudflared; its docker net/CIDR when containerized).
- **Caddy ALWAYS overwrites `X-Real-IP`.** Every environment's reverse_proxy
  block sets `header_up X-Real-IP {client_ip}` (prod blue/green, staging, QA —
  QA/staging front clients directly, where `{client_ip}` is simply the peer).
  This overwrite is the load-bearing half of the contract: the backend's
  preference for `X-Real-IP` is safe if and only if no client value can ride
  through the trusted hop.
- **The backend trusts, then verifies.** `trustedProxyHeaders` honors
  `X-Real-IP`/first-hop `X-Forwarded-For` only when (a) `TRUST_PROXY_HEADERS`
  is on, (b) the immediate peer is inside `TRUSTED_PROXY_CIDRS`, and (c) the
  value **parses via `net.ParseIP`**. Anything else falls back to the direct
  peer; a non-IP string is NEVER written into RemoteAddr (rate-limit keys,
  audit rows, session IP columns). From an untrusted peer the forwarded
  headers are stripped outright.

### 2. Global rate-limit keying: session hash, else client IP

The global 600/min throttle keys authenticated requests on a **seeded
`hash/maphash` of the session-cookie VALUE** — no DB lookup, fixed-size keys
(an attacker minting random cookies cannot allocate unbounded-length keys, and
the per-process random seed prevents offline collision crafting) — and
cookie-less requests on the resolved client IP. Signed-in users therefore
never share a bucket with the unauthenticated flood behind the same tunnel IP.
Only the on-box probes (`/api/health`, `/api/v1/version`) are exempt. The
streaming routes (`/api/events`, `/api/console/ws/`) are **no longer exempt**:
a stream counts once at open, so a random-cookie open-flood cannot exhaust the
DB pool through per-open session lookups; they remain exempt from the body cap
and request timeout.

### 3. Per-account second-factor counter

A failed TOTP/recovery attempt increments a per-account (user-id-keyed)
counter that a **correct password does not reset** — only a successful second
factor clears it. ~10 consecutive failures lock the 2FA STEP (not the account)
for a bounded 15-minute window; the streak decays after 15 idle minutes. This
closes the mint-a-fresh-challenge loop: password knowledge no longer buys
unlimited TOTP guesses.

### 4. Bounded, decaying login lockout

The per-account password lockout keys its maps on **SHA-256 of the
lowercased/trimmed email** (fixed-size keys; emails over RFC 5321's 254 bytes
are rejected before the limiter is touched), its failure streak **decays**
(reset after 15 idle minutes — never permanent), its lock escalates but **caps
at 15 minutes**, expired entries are pruned on write, and every limiter map is
**hard-capped at ~10k entries** (oldest-expiring evicted). The
check-before-password ordering is kept (no lockout-oracle timing).

## Consequences

- Spoofing the client identity now requires compromising the edge proxy
  itself; a mis-set `TRUSTED_PROXY_CIDRS` or `trusted_proxies` only
  over-counts (everyone as the proxy) — it never under-counts and never
  re-opens spoofing.
- Honest users behind the tunnel are isolated per session; probes
  (HEALTHCHECK, deploy gates) can never be starved by a flood.
- TOTP brute force is bounded at ~10 guesses per account per 15 minutes
  regardless of password knowledge, source-IP rotation, or challenge minting.
- Regression tests pin the backend half of the contract
  (`TestForwardedIPValidation`, `TestRateLimitSessionAndIPBuckets`,
  `TestRouterHealthNeverRateLimited`,
  `TestSecondFactorLockoutAcrossChallenges`, `TestAccountLockoutDecays`,
  `TestAccountMapBounded`); the Caddy half is configuration reviewed here and
  validated by `caddy validate` at deploy.

### Accepted residual: in-memory, per-instance lockout state

All limiter/lockout state (IP windows, account lockout, 2FA counter, global
throttle) is **in-memory per backend instance**. Blue/green runs one active
color, so in practice one instance enforces the bounds; a future multi-replica
deployment would multiply every budget by the replica count and lose state on
restart. A DB-backed lockout is an explicit open item in the findings register
(`docs/security/audit-2026-08-14.md`) — the per-challenge attempt counter
(`login_challenges.attempts`) is already DB-backed and caps a single challenge
at 5 attempts even across instances.

## Alternatives considered

- **Trust `X-Forwarded-For` exclusively and drop `X-Real-IP`.** XFF is
  append-semantics: the first hop is client-controlled unless every proxy in
  the chain is enumerated and stripped correctly; Cloudflare→cloudflared→Caddy
  makes that a three-party parsing contract. A single, edge-overwritten
  `X-Real-IP` is one assignment with one owner. Rejected for fragility.
- **Verify `CF-Connecting-IP` at the backend.** Couples the backend to
  Cloudflare and still requires trusting the intermediate hops not to inject
  it; the edge (Caddy) already has the authentic peer knowledge. Rejected.
- **Key the global limit on the verified session (DB lookup).** Correct but
  puts a DB query in front of EVERY request including floods — the exact
  amplification the limiter exists to prevent. The cookie-value hash gives a
  stable per-client bucket at zero lookups. Known trade-off: an attacker
  rotating random cookies mints fresh buckets and so is throttled per cookie,
  not in aggregate — but each such request costs the backend only a hash (no
  session lookup: the routes behind auth reject the bogus cookie with one
  indexed lookup at most once per request as before, and the previously
  exploitable stream-open lookups are now themselves rate-limited), keys are
  fixed-size, and the map is pruned. Accepted.
- **DB-backed lockout now.** Deferred (accepted residual above): correct
  multi-instance semantics need care (atomic upsert windows, decay in SQL) and
  the current deployment is single-active-instance; recorded as an open
  register item rather than half-built here.
