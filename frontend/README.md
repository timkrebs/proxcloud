# Proxcloud frontend

Next.js 15 (App Router) + TypeScript (strict) + Tailwind v4 + TanStack Query.
The frontend never talks to Proxmox directly — all `/api/*` traffic is proxied
to the Go backend (see `next.config.ts`).

## Getting started

```bash
npm run dev      # dev server on http://localhost:3000
npm run build    # production build (marketing routes prerender to static)
npm run lint
npm run test     # vitest
```

## Route groups: marketing vs. portal

The app is split into three App-Router route groups under `src/app/`:

- **`(marketing)`** — `/`, `/products`, `/solutions`, `/pricing`, `/support`,
  `/docs`. Public + static. No authenticated API, no portal chrome.
- **`(auth)`** — `/signin`, `/invite/[token]`. Unauthenticated entry points.
- **`(portal)`** — `/dashboard`, `/resources`, … (the signed-in console).
  Client-side auth: a `401` from `/api/*` bounces to `/signin` (`useMe()`).

- **Marketing is the public front door.** It renders the designed landing page
  and simple, on-brand sub-pages. It calls no authenticated API and does not
  import `<Providers>` or `PortalChrome`. See ADR-0021 for the decision record.
- **`src/middleware.ts`** redirects an authenticated visitor (one carrying the
  `proxcloud_session` cookie — presence only, no validation) off any marketing
  path to `/dashboard`. Its `matcher` covers **only** the six marketing paths, so
  portal/auth/api/static routing is untouched. The redirect decision is the pure,
  unit-tested `shouldRedirectToDashboard()`.

## Theme

Design tokens live once in `src/app/globals.css` as a Tailwind v4 `@theme`
block (`--color-*`, `--shadow-*`, radius, fonts). The **portal is light-only**
and never sets `data-theme`. The **marketing tree** adds a light/dark toggle:
the same `--color-*` names get `[data-theme="dark"]` overrides, applied only
under the marketing root wrapper (`.pc-marketing`, set by
`components/marketing/MarketingRoot.tsx`). The controller persists the choice to
`localStorage` and defaults to `prefers-color-scheme`, resolving the theme in a
pre-paint layout effect to avoid a flash. Never hardcode a hex a token owns.

## Marketing components

All marketing UI lives in `src/components/marketing/`:

- `MarketingRoot.tsx` — theme + toast providers and the themed root wrapper.
- `MarketingHeader.tsx` / `MarketingFooter.tsx` — shared chrome (mega-menu,
  mobile nav, theme toggle) rendered by `(marketing)/layout.tsx`.
- `sections.tsx` — the landing-page sections; `data.tsx` — all copy/links.
- `icons.tsx` / `mocks.tsx` — original inline-SVG product marks and decorative
  portal "screenshots" (no third-party assets).
- `CodeBlock.tsx`, `Reveal.tsx`, `PageShell.tsx` — interactive/shared leaves.

### Adding a marketing sub-page

1. Create `src/app/(marketing)/<name>/page.tsx`. Keep it a Server Component so it
   prerenders to static (push any interactivity into a small `"use client"`
   leaf). Reuse `PageShell` / `FeatureGrid` / `InfoCard` from
   `@/components/marketing/PageShell` for consistent chrome.
2. `export const metadata` with a `title` + `description`.
3. Use token utility classes only (no inline `style=`, no raw hex). CTAs must
   point at real destinations — an in-app route, an in-page anchor (`/#services`,
   `/#features`, `/#how`, `/#api`, `/#pricing`, `/#top`), `/signin`, or the
   GitHub URL. No dead `#` links.
4. If the page should be reachable from the nav/footer, add it to the relevant
   array in `data.tsx` (`navLinks`, `mobileLinks`, `footerCols`).
5. **Add the path to `src/middleware.ts`** — both `MARKETING_PATHS` and the
   `config.matcher` — so authenticated visitors are redirected off it, and add a
   case to `src/middleware.test.ts`.
