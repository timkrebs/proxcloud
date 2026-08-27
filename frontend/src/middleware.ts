// Edge middleware: keep authenticated visitors out of the public marketing
// pages. The marketing tree is public + static and calls no authenticated API,
// and the app has no server session store to validate against at the edge, so
// we key on cookie *presence* only (ADR-0021). Session *validity* stays checked
// downstream exactly as today: a stale cookie lands on /dashboard, whose
// useMe() 401s and bounces to /signin. The matcher is scoped to marketing paths
// only, so portal/auth/api/static routing is untouched.
import { NextResponse } from "next/server";
import type { NextRequest } from "next/server";

/** Name of the opaque session cookie the backend sets (backend auth.CookieName). */
export const SESSION_COOKIE = "proxcloud_session";

/** The exact set of public marketing paths this middleware governs. */
export const MARKETING_PATHS = [
  "/",
  "/products",
  "/solutions",
  "/pricing",
  "/support",
  "/docs",
] as const;

const MARKETING_PATH_SET = new Set<string>(MARKETING_PATHS);

/** True when the path is one of the public marketing pages. */
export function isMarketingPath(pathname: string): boolean {
  return MARKETING_PATH_SET.has(pathname);
}

/**
 * Pure decision function (unit-tested): an authenticated visitor on a marketing
 * path is redirected to the portal; everyone else passes through.
 */
export function shouldRedirectToDashboard(pathname: string, hasSession: boolean): boolean {
  return hasSession && isMarketingPath(pathname);
}

export function middleware(request: NextRequest): NextResponse {
  const hasSession = request.cookies.has(SESSION_COOKIE);
  if (shouldRedirectToDashboard(request.nextUrl.pathname, hasSession)) {
    const url = request.nextUrl.clone();
    url.pathname = "/dashboard";
    url.search = "";
    return NextResponse.redirect(url);
  }
  return NextResponse.next();
}

// Matcher covers ONLY marketing paths — never /dashboard, /signin, /api/*,
// /invite/*, or static assets. Keep this in sync with MARKETING_PATHS.
export const config = {
  matcher: ["/", "/products", "/solutions", "/pricing", "/support", "/docs"],
};
