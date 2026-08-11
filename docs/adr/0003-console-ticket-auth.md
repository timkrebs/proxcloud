# ADR-0003: Console via ticket auth + one-shot backend WS bridge

Date: 2026-08-11 · Status: accepted

## Context

Proxmox's `vncwebsocket`/`termproxy` endpoints reject `PVEAPIToken`
authentication (verified against community reports and go-proxmox's own
`ErrAPITokenWebSocketUnsupported`). The embedded console cannot work
with the API token alone, and Proxmox credentials must never reach the
browser.

## Decision

- Optional second credential pair `PROXMOX_CONSOLE_USER`/`_PASSWORD` →
  `POST /access/ticket` (cached, refreshed at 90 min of its 2 h life).
  Unset ⇒ the Console blade renders an explicit disabled state.
- `POST .../console` creates the vncproxy (websocket + one-time
  password) or termproxy ticket and stores a **one-shot session**:
  128-bit random id, single use, 25 s TTL.
- `GET /api/console/ws/{sessionId}` upgrades the browser connection and
  pipes frames to PVE's `vncwebsocket`, dialed with the `PVEAuthCookie`
  over a forced-HTTP/1.1 TLS config (the shared transport advertises h2,
  which PVE answers with an h2 frame instead of a 101). The **one-shot
  session id is the credential**: Next rewrites cannot proxy websockets,
  so the browser hits the backend origin directly, and in dev that origin
  is `ws://` while the portal session cookie is `Secure` — the browser
  legitimately omits it. The portal cookie is therefore checked only
  *advisorily* (a presented cookie must be genuine; an absent one is fine),
  and the session record carries the issuing user for audit. The route is
  exempt from the request timeout; origin checks, a concurrent-bridge cap,
  1 MiB read limits, and 60 s idle deadlines guard it.
- termproxy's `user:ticket\n` handshake is written by the backend so the
  ticket never reaches the client; noVNC authenticates RFB with the
  one-time password only.

## Consequences

Console is feature-flagged and honest when unavailable; the only
credential the browser ever holds is a single-connection VNC password.
