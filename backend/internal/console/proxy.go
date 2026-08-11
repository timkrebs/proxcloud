package console

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
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
	// AllowedOrigins guards against cross-site websocket hijacking; empty
	// entries are ignored. localhost origins are always allowed in dev.
	AllowedOrigins []string
}

func (p *Proxy) upgrader() *websocket.Upgrader {
	return &websocket.Upgrader{
		ReadBufferSize:  32 << 10,
		WriteBufferSize: 32 << 10,
		Subprotocols:    []string{"binary"},
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true // non-browser clients (tests)
			}
			u, err := url.Parse(origin)
			if err != nil {
				return false
			}
			host := u.Hostname()
			if host == "localhost" || host == "127.0.0.1" {
				return true
			}
			for _, allowed := range p.AllowedOrigins {
				if allowed != "" && strings.EqualFold(origin, allowed) {
					return true
				}
			}
			return false
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
	sess, ok := p.Sessions.Claim(id)
	if !ok {
		http.Error(w, "unknown or expired console session", http.StatusNotFound)
		return
	}

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

	dialer := &websocket.Dialer{
		TLSClientConfig:  p.Auth.Transport().TLSClientConfig,
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

	client, err := p.upgrader().Upgrade(w, r, nil)
	if err != nil {
		p.Log.Error("console: upgrade", "err", err)
		return
	}
	defer client.Close()

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
