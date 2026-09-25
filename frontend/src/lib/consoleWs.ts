// Console websocket URL builder — pure so the origin/downgrade rules are
// unit-testable without rendering the console page.

/** The slice of window.location the URL builder needs. */
export interface WsLocation {
  protocol: string;
  /** Hostname including the port when present (window.location.host). */
  host: string;
}

/**
 * Build the console websocket URL for a one-shot session id.
 *
 * When NEXT_PUBLIC_BACKEND_WS_ORIGIN is unset or empty, the origin defaults
 * to same-origin (`ws(s)://<host>`): every deployment fronts the app with a
 * proxy (Caddy) that forwards /api/* — including /api/console/ws/ — to the
 * backend with websocket upgrade support, while the backend's own port is
 * bound to loopback and unreachable from the browser.
 */
export function consoleWsUrl(
  sessionId: string,
  envOrigin: string | undefined,
  loc: WsLocation | null,
): string {
  const secure = loc !== null && loc.protocol === "https:";
  let origin = envOrigin || (loc !== null ? `${secure ? "wss" : "ws"}://${loc.host}` : "");
  // Never downgrade: an HTTPS page must not carry the console (and its
  // one-shot credential) over cleartext ws://.
  if (secure && origin.startsWith("ws://")) {
    origin = "wss://" + origin.slice("ws://".length);
  }
  return `${origin}/api/console/ws/${encodeURIComponent(sessionId)}`;
}
