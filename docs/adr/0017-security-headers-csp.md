# ADR-0017 — Security headers & Content-Security-Policy

- **Status:** Accepted (2026-08-14)
- **Context:** public-exposure hardening pass (`docs/security/audit-2026-08-14.md`, findings H1/M1).

## Context

Proxcloud is now publicly reachable (`portal.proxcloud.io`). It ships **no**
security response headers today — no CSP, `X-Frame-Options`, HSTS,
`X-Content-Type-Options`, or `Permissions-Policy` (only `Referrer-Policy` on
`/invite`). For a control plane that holds a Proxmox token able to create and
destroy real infrastructure, that means: the portal is **clickjackable** (any
page can iframe it and trick an authenticated victim into a VM delete or invite
accept); any reflected/stored XSS or one compromised frontend dependency runs
with full privilege and can **exfiltrate to any origin**; and the origin is
**downgradeable** (no HSTS at the app layer — we may not rely on Cloudflare).
The origin is assumed directly reachable (LAN / tunnel bypass), so every header
must be set at the application layer.

## Decision

Set security headers in **two places**, because two servers answer:

1. **Frontend (Next.js `next.config.ts` `headers()`)** — the portal HTML the
   browser navigates to. Applied to `/(.*)`:
   - **Content-Security-Policy** (see directives below)
   - `Strict-Transport-Security: max-age=63072000; includeSubDomains; preload`
   - `X-Content-Type-Options: nosniff`
   - `X-Frame-Options: DENY`
   - `Referrer-Policy: strict-origin-when-cross-origin` (`/invite` keeps `no-referrer`)
   - `Permissions-Policy: camera=(), microphone=(), geolocation=(), interest-cohort=()`

2. **Backend (`httpserver.securityHeaders` middleware)** — the directly-reachable
   API + console-WS surface. Sets `nosniff`, `X-Frame-Options: DENY`, HSTS,
   `Referrer-Policy`, and a locked-down CSP `default-src 'none'; frame-ancestors
   'none'; base-uri 'none'` (API responses are data, never rendered).

### CSP directives (portal)

```
default-src 'self';
script-src 'self' 'unsafe-inline';
style-src 'self' 'unsafe-inline';
img-src 'self' data:;
font-src 'self';
connect-src 'self';
frame-ancestors 'none';
base-uri 'self';
object-src 'none';
form-action 'self'
```

- **`connect-src 'self'`** is the load-bearing control: the app is same-origin
  (browser → portal host; Next proxies `/api`, Caddy proxies the console WS), so
  even a successful script injection could not exfiltrate to a foreign origin.
- **`frame-ancestors 'none'`** (+ `X-Frame-Options: DENY`) stops clickjacking of
  the destructive controls.
- **`object-src 'none'`, `base-uri 'self'`, `form-action 'self'`** close common
  injection/redirection vectors.

## Host allowlist (M1)

The backend adds a `hostAllowlist` middleware: when `ALLOWED_HOSTS` is set, a
request whose `Host` is not in the list is rejected `421 Misdirected Request`
(DNS-rebinding / host-injection defense). Empty disables it (dev). Production
sets `ALLOWED_HOSTS=portal.proxcloud.io,portal.staging.proxcloud.io` plus the
internal compose service names.

## Residual / follow-ups

- **`script-src`/`style-src` keep `'unsafe-inline'`.** Next's App Router emits
  inline hydration/bootstrap scripts and inline styles; a strict CSP requires a
  **nonce-based** policy wired through Next middleware. That is tracked as a
  follow-up. Note the mitigation is only partial there — but `connect-src 'self'`
  still prevents exfiltration and `frame-ancestors 'none'` still prevents
  framing, which are the highest-impact protections.
- **Cloudflare Access** (ADR pending) would add an identity-aware pre-auth layer;
  these app-layer headers hold regardless.

## Consequences

- Clickjacking, MIME-sniffing, and app-layer TLS downgrade are closed; XSS
  exfiltration is contained by `connect-src`.
- Tests: `TestSecurityHeaders` (backend headers present) and `TestHostAllowlist`
  (unknown Host → 421) are the regression guards; the frontend headers are
  validated by the Next build.
