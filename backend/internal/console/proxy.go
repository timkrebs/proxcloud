package console

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	dialTimeout  = 15 * time.Second
	idleDeadline = 60 * time.Second
)

// Proxy serves GET /api/console/ws/{sessionId}: upgrades the browser
// connection and pipes frames to PVE's vncwebsocket. Auth is the one-shot
// session id (unguessable, single-use, 25s TTL) — deliberately cookie-free
// because the browser connects to the backend origin directly (Next
// rewrites cannot proxy websockets).
type Proxy struct {
	Auth     *TicketAuth
	Sessions *Sessions
	Log      *slog.Logger
	// FrontendOrigin, when set, is the only Origin accepted (and an Origin
	// header becomes mandatory); otherwise localhost origins are allowed
	// for dev.
	FrontendOrigin string
	// VerifyCookie, when set, must accept the request's portal session —
	// binds the one-shot console id to a signed-in browser wherever the
	// backend is cookie-reachable (the default single-host setup).
	VerifyCookie func(r *http.Request) bool

	mu     sync.Mutex
	active int
}

// maxBridges caps concurrent console bridges (memory/FD protection).
const maxBridges = 8

// wsReadLimit bounds a single websocket frame from either side.
const wsReadLimit = 1 << 20

func (p *Proxy) upgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		ReadBufferSize:  32 << 10,
		WriteBufferSize: 32 << 10,
		Subprotocols:    []string{"binary"},
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if p.FrontendOrigin != "" {
				// Configured deployment: exact match required, no
				// header means no console.
				return strings.EqualFold(origin, p.FrontendOrigin)
			}
			if origin == "" {
				return true // dev: non-browser clients (tests)
			}
			u, err := url.Parse(origin)
			if err != nil {
				return false
			}
			host := u.Hostname()
			return host == "localhost" || host == "127.0.0.1"
		},
	}
}

// ServeHTTP handles the websocket bridge.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sessionId")
	if id == "" {
		// chi routing passes the param via context; fall back to URL tail.
		parts := strings.Split(r.URL.Path, "/")
		id = parts[len(parts)-1]
	}
	if p.VerifyCookie != nil && !p.VerifyCookie(r) {
		http.Error(w, "console requires a signed-in session", http.StatusUnauthorized)
		return
	}
	sess, ok := p.Sessions.Claim(id)
	if !ok {
		http.Error(w, "unknown or expired console session", http.StatusNotFound)
		return
	}

	p.mu.Lock()
	if p.active >= maxBridges {
		p.mu.Unlock()
		http.Error(w, "too many concurrent console sessions", http.StatusServiceUnavailable)
		return
	}
	p.active++
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
	}()

	// Dial PVE first so an upstream failure yields a clean HTTP error.
	ticket, _, err := p.Auth.Ticket(r.Context())
	if err != nil {
		p.Log.Error("console: ticket", "err", err)
		http.Error(w, "console authentication failed", http.StatusBadGateway)
		return
	}

	base := strings.Replace(p.Auth.BaseURL(), "https://", "wss://", 1)
	base = strings.Replace(base, "http://", "ws://", 1)
	target := fmt.Sprintf("%s/nodes/%s/%s/%d/vncwebsocket?port=%s&vncticket=%s",
		base, url.PathEscape(sess.Node), sess.GuestType, sess.VMID,
		url.QueryEscape(sess.Proxy.Port), url.QueryEscape(sess.Proxy.Ticket))

	// The shared HTTP transport advertises HTTP/2 in its TLS ALPN; reusing
	// that config makes PVE answer the websocket upgrade with an h2 frame.
	// Clone it and force HTTP/1.1 for the websocket handshake.
	var tlsCfg *tls.Config
	if base := p.Auth.Transport().TLSClientConfig; base != nil {
		tlsCfg = base.Clone()
	} else {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tlsCfg.NextProtos = []string{"http/1.1"}

	dialer := &websocket.Dialer{
		TLSClientConfig:  tlsCfg,
		HandshakeTimeout: dialTimeout,
		Subprotocols:     []string{"binary"},
	}
	header := http.Header{}
	header.Set("Cookie", "PVEAuthCookie="+ticket)

	ctx, cancel := context.WithTimeout(r.Context(), dialTimeout)
	upstream, res, err := dialer.DialContext(ctx, target, header)
	cancel()
	if err != nil {
		status := ""
		if res != nil {
			status = res.Status
		}
		p.Log.Error("console: dial pve", "err", err, "status", status)
		http.Error(w, "could not open the Proxmox console websocket", http.StatusBadGateway)
		return
	}
	defer upstream.Close()
	upstream.SetReadLimit(wsReadLimit)

	client, err := p.upgrader().Upgrade(w, r, nil)
	if err != nil {
		p.Log.Error("console: upgrade", "err", err)
		return
	}
	defer client.Close()
	client.SetReadLimit(wsReadLimit)

	// termproxy expects "user:ticket\n" as the first client message; the
	// backend performs it so the PVE ticket never reaches the browser.
	if sess.Kind == "term" {
		auth := fmt.Sprintf("%s:%s\n", sess.AuthUser, sess.Proxy.Ticket)
		if err := upstream.WriteMessage(websocket.BinaryMessage, []byte(auth)); err != nil {
			p.Log.Error("console: term auth", "err", err)
			return
		}
	}

	p.Log.Info("console session open", "kind", sess.Kind, "guest", fmt.Sprintf("%s/%d", sess.GuestType, sess.VMID))
	errc := make(chan error, 2)
	go pipe(client, upstream, errc)
	go pipe(upstream, client, errc)
	<-errc
	p.Log.Info("console session closed", "guest", fmt.Sprintf("%s/%d", sess.GuestType, sess.VMID))
}

// pipe copies frames dst←src until error, refreshing the idle deadline on
// every message.
func pipe(dst, src *websocket.Conn, errc chan<- error) {
	for {
		_ = src.SetReadDeadline(time.Now().Add(idleDeadline))
		mt, data, err := src.ReadMessage()
		if err != nil {
			errc <- err
			return
		}
		_ = dst.SetWriteDeadline(time.Now().Add(idleDeadline))
		if err := dst.WriteMessage(mt, data); err != nil {
			errc <- err
			return
		}
	}
}
