// Console websocket URL contract — same-origin default (Caddy proxies
// /api/* with websocket upgrade), explicit-origin override, and the
// never-downgrade guard for HTTPS pages.
import { describe, expect, it } from "vitest";

import { consoleWsUrl } from "@/lib/consoleWs";

describe("consoleWsUrl", () => {
  it("env unset + http page → same-origin ws:// with the page's host:port", () => {
    expect(consoleWsUrl("sess-1", undefined, { protocol: "http:", host: "portal.lan:3000" })).toBe(
      "ws://portal.lan:3000/api/console/ws/sess-1",
    );
  });

  it("env empty + http page → same-origin ws:// (empty is treated as unset)", () => {
    expect(consoleWsUrl("sess-1", "", { protocol: "http:", host: "portal.lan:3000" })).toBe(
      "ws://portal.lan:3000/api/console/ws/sess-1",
    );
  });

  it("env unset + https page → same-origin wss://", () => {
    expect(
      consoleWsUrl("sess-2", undefined, { protocol: "https:", host: "cloud.example.com" }),
    ).toBe("wss://cloud.example.com/api/console/ws/sess-2");
  });

  it("env set → env origin used unchanged", () => {
    expect(
      consoleWsUrl("sess-3", "wss://ws.example.com:9443", { protocol: "http:", host: "portal.lan" }),
    ).toBe("wss://ws.example.com:9443/api/console/ws/sess-3");
  });

  it("env set to ws:// on an https page → upgraded to wss:// (never downgrade)", () => {
    expect(
      consoleWsUrl("sess-4", "ws://backend.lan:8080", {
        protocol: "https:",
        host: "cloud.example.com",
      }),
    ).toBe("wss://backend.lan:8080/api/console/ws/sess-4");
  });

  it("env set to ws:// on an http page → left as ws://", () => {
    expect(
      consoleWsUrl("sess-5", "ws://backend.lan:8080", { protocol: "http:", host: "portal.lan" }),
    ).toBe("ws://backend.lan:8080/api/console/ws/sess-5");
  });

  it("url-encodes the session id", () => {
    expect(consoleWsUrl("a/b c", undefined, { protocol: "http:", host: "portal.lan" })).toBe(
      "ws://portal.lan/api/console/ws/a%2Fb%20c",
    );
  });
});
