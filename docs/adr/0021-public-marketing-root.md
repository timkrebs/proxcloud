# ADR-0021: Public marketing root & the marketing/portal split

Date: 2026-08-27 · Status: accepted

## Context

Proxcloud needs a public **marketing landing page** — the front door that sells
the product and drives visitors to the portal — served as the unauthenticated
root of the *existing* Next.js app (not a separate site). The portal (the
signed-in Azure-style console) must keep its current routes, auth model, and
deep links unchanged. Today `frontend/src/app/page.tsx` is already a bare public
Server Component landing; this wave replaces it with the full designed page and
formalizes the split.

Constraints from the existing app (confirmed in code):
- **Auth is client-side only.** No `middleware.ts`, no server session gate. A
  `401` from any `/api/*` (except `/api/auth/*`) triggers `apiFetch` to
  `window.location.href = "/signin"` (`src/lib/api/client.ts`); portal pages are
  protected implicitly because their data fetches fail. Sign-in = `/signin`,
  authed landing = `/dashboard`.
- **The portal is light-mode only.** Tokens live in `globals.css` as a Tailwind
  v4 `@theme` block (Fluent palette; `--color-accent #0078d4`, etc.). No theme
  toggle, no dark tokens, no `tailwind.config`.
- **No CSP** anywhere (frontend or backend) on this branch.

## Decision

- **Marketing lives in a `(marketing)` route group** with its own layout,
  cleanly separated from `(portal)` and `(auth)`, but sharing the ONE token
  theme (`globals.css`) and genuinely-common primitives (`Button`, `icons`). The
  public marketing pages are the app root `/` plus `/products`, `/solutions`,
  `/pricing`, `/support`, `/docs` (real routes — simple stubs where content isn't
  ready, never dead `#` links). CTAs point at the existing `/signin` and
  `/dashboard`; `/docs` is an in-app stub for now.

- **Redirect authenticated users off the public marketing pages via a thin
  `middleware.ts` cookie-presence check.** The middleware redirects a request to
  a marketing path to `/dashboard` **iff** the `proxcloud_session` cookie is
  present. Rationale: the marketing page must not call an authenticated API
  (it's public + static), and the app has no server session store to validate
  against at the edge — so we key on cookie *presence* only. Session *validity*
  stays checked downstream exactly as today: a stale cookie lands the user on
  `/dashboard`, whose `useMe()` 401s and bounces to `/signin`. This adds the one
  piece the model lacks (an authed→portal redirect) without inventing a parallel
  auth system or leaking data. The middleware matcher covers only the marketing
  paths, so portal/auth routing is untouched.

- **The marketing area gets its own light/dark theme toggle; the portal stays
  light-only.** The design ships dark tokens and a footer toggle. We add the
  dark values as `[data-theme="dark"]` overrides of the SAME `--color-*` token
  names, scoped so only the `(marketing)` tree opts in (a small client theme
  controller: `data-theme` on the marketing root, persisted to `localStorage`,
  defaulting to `prefers-color-scheme`, reduced-motion respected). The portal
  renders unaffected (it never sets `data-theme`, so it uses the light `:root`
  values). One token set, extended with dark values for marketing.

- **Static-first & SEO.** The marketing routes are statically rendered (no
  per-user data, no authed API), with full Metadata API output (title,
  description, canonical `https://portal.proxcloud.io`, Open Graph + Twitter) and
  a generated `opengraph-image` (Next `ImageResponse`, no raster asset). Original
  inline-SVG portal mocks only — no copied third-party (Azure) assets.

- **CSP-friendly by construction.** Tailwind utility classes + inline SVG + React
  event handlers (no inline `on*=` HTML, no inline `<script>`), so a future
  hardening CSP fits without `unsafe-inline` beyond what the app already needs.

## Consequences

- Signed-out visitors get the marketing page at `/`; signed-in visitors are
  bounced to `/dashboard` by the middleware — they never see the marketing page.
- The portal's routing, client-side auth model, and deep links are unchanged; the
  middleware matcher is scoped to marketing paths only.
- One shared design-token theme keeps landing and portal visually one product;
  the marketing-only dark mode never affects the light portal.
- The `proxcloud_session` cookie name is now depended on by the frontend edge
  middleware (previously only the backend knew it) — a small coupling, documented
  here; a rename would need both sides updated.
- Marketing pages are public, static, and call no authenticated API — no new
  CORS/auth surface, no cross-tenant data.

## Alternatives considered

- **Client-side `useMe()` redirect on `/`.** Rejected: it makes the public page
  call an authenticated API (violates "no authed API on the public route"), adds
  a flash of marketing content before redirect, and needs `<Providers>` on the
  public root.
- **Server-validated session in middleware.** Rejected: there's no shared session
  store at the Next edge; validating would mean an internal call to the Go
  backend per request. Cookie-presence + downstream validity is the pragmatic,
  model-consistent choice.
- **A separate marketing site/repo.** Rejected by the brief: it must be the
  same-origin unauthenticated root of the existing app, sharing one token set.
- **Porting the portal to a global theme provider for dark mode.** Out of scope
  and risky; the portal is deliberately light-only. Dark mode is scoped to
  marketing.
