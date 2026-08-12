"use client";
// Console blade — noVNC for VMs, xterm for containers, both through the
// backend websocket bridge (one-shot session ids; no Proxmox credential
// ever reaches the browser). Honest disabled state when the backend has
// no console credentials.
import { useCallback, useEffect, useRef, useState } from "react";
import "@xterm/xterm/css/xterm.css";

import { CardError } from "@/components/dashboard/DashboardCards";
import { BladeHeading, useGuestParams } from "@/components/guest/common";
import { Button } from "@/components/ui/Button";
import { EmptyState } from "@/components/ui/EmptyState";
import { Spinner } from "@/components/ui/icons";
import { ApiError, apiFetch } from "@/lib/api/client";
import type { ConsoleSession } from "@/lib/api/generated/types";
import { useGuest, type GuestParams } from "@/lib/api/guestQueries";
import { useActiveTenantId } from "@/lib/stores/uiStore";

function wsUrl(sessionId: string): string {
  const secure = typeof window !== "undefined" && window.location.protocol === "https:";
  let origin =
    process.env.NEXT_PUBLIC_BACKEND_WS_ORIGIN ??
    (typeof window !== "undefined"
      ? `${secure ? "wss" : "ws"}://${window.location.hostname}:8080`
      : "");
  // Never downgrade: an HTTPS page must not carry the console (and its
  // one-shot credential) over cleartext ws://.
  if (secure && origin.startsWith("ws://")) {
    origin = "wss://" + origin.slice("ws://".length);
  }
  return `${origin}/api/console/ws/${encodeURIComponent(sessionId)}`;
}

async function openSession(
  tenantId: string,
  g: GuestParams,
  kind: "vnc" | "term",
): Promise<ConsoleSession> {
  return apiFetch<ConsoleSession>(
    `/api/tenants/${tenantId}/guests/${encodeURIComponent(g.node)}/${g.type}/${g.vmid}/console`,
    {
      method: "POST",
      body: JSON.stringify({ kind }),
    },
  );
}

type Phase = "idle" | "connecting" | "connected" | "closed" | "error";

function VncConsole({ g, tenantId }: { g: GuestParams; tenantId: string }) {
  const el = useRef<HTMLDivElement>(null);
  const rfbRef = useRef<{ disconnect(): void } | null>(null);
  const didInit = useRef(false);
  const [phase, setPhase] = useState<Phase>("idle");
  const [error, setError] = useState<unknown>(null);

  const connect = useCallback(async () => {
    // Tear down any prior client first so a reconnect never leaves two noVNC
    // clients / two vncproxy sessions racing: each vncproxy resets the guest's
    // one-time VNC password, and a stale client then fails RFB auth
    // ("Security negotiation failed / Authentication failed").
    rfbRef.current?.disconnect();
    rfbRef.current = null;
    setPhase("connecting");
    setError(null);
    try {
      const sess = await openSession(tenantId, g, "vnc");
      const { default: RFB } = await import("@novnc/novnc");
      if (!el.current) return;
      const rfb = new RFB(el.current, wsUrl(sess.sessionId), {
        credentials: { username: "", password: sess.password ?? "", target: "" },
        wsProtocols: ["binary"],
      });
      rfb.scaleViewport = true;
      rfb.addEventListener("connect", () => setPhase("connected"));
      rfb.addEventListener("disconnect", () => setPhase("closed"));
      rfbRef.current = rfb;
    } catch (err) {
      setError(err);
      setPhase("error");
    }
  }, [g, tenantId]);

  useEffect(() => {
    // React StrictMode (dev) runs mount effects twice; guard so we open exactly
    // one console session (two vncproxy calls would race on the guest's VNC
    // password and surface a spurious "Authentication failed").
    if (didInit.current) return;
    didInit.current = true;
    connect();
    return () => rfbRef.current?.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div>
      {phase === "connecting" ? (
        <p className="mb-2 flex items-center gap-2 text-[13px] text-ink-2">
          <Spinner size={14} /> Opening VNC session…
        </p>
      ) : null}
      {phase === "closed" ? (
        <div className="mb-2 flex items-center gap-3 text-[13px] text-ink-2">
          Console session ended.
          <Button variant="secondaryCompact" onClick={connect}>
            Reconnect
          </Button>
        </div>
      ) : null}
      {phase === "error" ? <ConsoleError err={error} retry={connect} /> : null}
      <div
        ref={el}
        className="h-[70vh] min-h-[420px] w-full overflow-hidden rounded-fluent border border-line bg-[#000]"
      />
    </div>
  );
}

function TermConsole({ g, tenantId }: { g: GuestParams; tenantId: string }) {
  const el = useRef<HTMLDivElement>(null);
  const wsRef = useRef<WebSocket | null>(null);
  const didInit = useRef(false);
  const [phase, setPhase] = useState<Phase>("idle");
  const [error, setError] = useState<unknown>(null);

  const connect = useCallback(async () => {
    wsRef.current?.close(); // drop any prior session before opening a new one
    setPhase("connecting");
    setError(null);
    try {
      const sess = await openSession(tenantId, g, "term");
      const [{ Terminal }, { FitAddon }] = await Promise.all([
        import("@xterm/xterm"),
        import("@xterm/addon-fit"),
      ]);
      if (!el.current) return;
      el.current.innerHTML = "";

      const term = new Terminal({ cursorBlink: true, fontSize: 13, fontFamily: "Cascadia Code, Consolas, monospace" });
      const fit = new FitAddon();
      term.loadAddon(fit);
      term.open(el.current);
      fit.fit();

      const ws = new WebSocket(wsUrl(sess.sessionId), ["binary"]);
      ws.binaryType = "arraybuffer";
      wsRef.current = ws;

      const sendResize = () => {
        fit.fit();
        if (ws.readyState === WebSocket.OPEN) ws.send(`1:${term.cols}:${term.rows}:`);
      };

      ws.onopen = () => {
        setPhase("connected");
        sendResize();
      };
      ws.onclose = () => setPhase("closed");
      ws.onerror = () => setPhase((p) => (p === "connected" ? "closed" : "error"));
      ws.onmessage = (ev) => {
        const data = typeof ev.data === "string" ? ev.data : new TextDecoder().decode(ev.data);
        // The PVE term stream replies "OK" to the backend's auth line first.
        term.write(data.startsWith("OK") && term.buffer.active.length <= 1 ? data.slice(2) : data);
      };

      term.onData((data) => {
        if (ws.readyState === WebSocket.OPEN) ws.send(`0:${new TextEncoder().encode(data).length}:${data}`);
      });
      const ping = setInterval(() => {
        if (ws.readyState === WebSocket.OPEN) ws.send("2");
      }, 30_000);
      const onResize = () => sendResize();
      window.addEventListener("resize", onResize);
      ws.addEventListener("close", () => {
        clearInterval(ping);
        window.removeEventListener("resize", onResize);
      });
    } catch (err) {
      setError(err);
      setPhase("error");
    }
  }, [g, tenantId]);

  useEffect(() => {
    // StrictMode (dev) runs mount effects twice; open exactly one term session.
    if (didInit.current) return;
    didInit.current = true;
    connect();
    return () => wsRef.current?.close();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <div>
      {phase === "connecting" ? (
        <p className="mb-2 flex items-center gap-2 text-[13px] text-ink-2">
          <Spinner size={14} /> Opening terminal session…
        </p>
      ) : null}
      {phase === "closed" ? (
        <div className="mb-2 flex items-center gap-3 text-[13px] text-ink-2">
          Terminal session ended.
          <Button variant="secondaryCompact" onClick={connect}>
            Reconnect
          </Button>
        </div>
      ) : null}
      {phase === "error" ? <ConsoleError err={error} retry={connect} /> : null}
      <div ref={el} className="h-[70vh] min-h-[420px] w-full rounded-fluent border border-line bg-[#000] p-1" />
    </div>
  );
}

function ConsoleError({ err, retry }: { err: unknown; retry: () => void }) {
  if (err instanceof ApiError && err.code === "console_disabled") {
    return (
      <EmptyState
        icon="console"
        title="Console is disabled"
        body="Proxmox rejects API-token auth on its console websockets, so the embedded console needs PROXMOX_CONSOLE_USER and PROXMOX_CONSOLE_PASSWORD in the backend environment. See the README for setup."
      />
    );
  }
  return (
    <div className="mb-2 flex items-center gap-3">
      <CardError err={err} />
      <Button variant="secondaryCompact" onClick={retry}>
        Retry
      </Button>
    </div>
  );
}

export default function ConsolePage() {
  const g = useGuestParams();
  const guest = useGuest(g);
  const tenantId = useActiveTenantId();

  if (tenantId === null) {
    return (
      <div>
        <BladeHeading>Console</BladeHeading>
        <p className="flex items-center gap-2 text-[13px] text-ink-2">
          <Spinner size={14} /> Loading directory…
        </p>
      </div>
    );
  }

  if (guest.data && guest.data.status !== "running") {
    return (
      <div>
        <BladeHeading>Console</BladeHeading>
        <EmptyState
          icon="console"
          title="Guest is not running"
          body="Start the guest from the command bar above, then connect to its console."
        />
      </div>
    );
  }

  return (
    <div>
      <BladeHeading sub={g.type === "qemu" ? "noVNC" : "xterm.js"}>Console</BladeHeading>
      {g.type === "qemu" ? <VncConsole g={g} tenantId={tenantId} /> : <TermConsole g={g} tenantId={tenantId} />}
    </div>
  );
}
