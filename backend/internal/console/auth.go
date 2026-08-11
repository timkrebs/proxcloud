// Package console implements the embedded-console path: Proxmox's
// vncwebsocket/termproxy endpoints reject API-token auth, so this package
// keeps an optional username/password ticket session (PVEAuthCookie),
// creates one-shot proxy sessions, and pipes the websocket through the
// backend so no Proxmox credential ever reaches the browser.
package console

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
)

const (
	// PVE tickets live 2h; refresh at 90min so a console opened near the
	// boundary still authenticates.
	ticketLifetime = 90 * time.Minute
	authTimeout    = 10 * time.Second
)

// ErrDisabled marks the honest "console credentials not configured" state.
var ErrDisabled = &types.APIError{
	Code:    "console_disabled",
	Message: "The console needs PROXMOX_CONSOLE_USER and PROXMOX_CONSOLE_PASSWORD — Proxmox websockets reject API tokens. See the README.",
	Status:  http.StatusServiceUnavailable,
}

// TicketAuth maintains a PVE username/password ticket session.
type TicketAuth struct {
	baseURL  string
	user     string
	password string
	client   *http.Client

	mu        sync.Mutex
	ticket    string
	csrf      string
	fetchedAt time.Time
}

// NewTicketAuth returns nil when console credentials are not configured.
func NewTicketAuth(cfg *config.Config) *TicketAuth {
	if !cfg.ConsoleEnabled() {
		return nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.ProxmoxTLSInsecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}
	}
	return &TicketAuth{
		baseURL:  cfg.ProxmoxURL + "/api2/json",
		user:     cfg.ConsoleUser,
		password: cfg.ConsolePassword,
		client:   &http.Client{Transport: transport, Timeout: authTimeout},
	}
}

// Transport returns the underlying HTTP transport, shared with the
// websocket dialer so TLS settings stay consistent.
func (t *TicketAuth) Transport() *http.Transport {
	return t.client.Transport.(*http.Transport)
}

// BaseURL returns the PVE API base (…/api2/json).
func (t *TicketAuth) BaseURL() string { return t.baseURL }

// Ticket returns a valid PVEAuthCookie ticket + CSRF token, refreshing the
// session when stale.
func (t *TicketAuth) Ticket(ctx context.Context) (ticket, csrf string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ticket != "" && time.Since(t.fetchedAt) < ticketLifetime {
		return t.ticket, t.csrf, nil
	}

	body, _ := json.Marshal(map[string]string{"username": t.user, "password": t.password})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL+"/access/ticket", bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")

	res, err := t.client.Do(req)
	if err != nil {
		return "", "", &types.APIError{Code: "proxmox_unreachable", Message: "Could not reach Proxmox for console authentication.", PVEMessage: err.Error(), Status: http.StatusBadGateway}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return "", "", &types.APIError{
			Code:       "proxmox_auth_failed",
			Message:    "Console login was rejected by Proxmox — check PROXMOX_CONSOLE_USER/PASSWORD.",
			PVEMessage: fmt.Sprintf("%s: %s", res.Status, bytes.TrimSpace(raw)),
			Status:     http.StatusBadGateway,
		}
	}
	var parsed struct {
		Data struct {
			Ticket string `json:"ticket"`
			CSRF   string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Data.Ticket == "" {
		return "", "", fmt.Errorf("console: malformed ticket response")
	}
	t.ticket, t.csrf, t.fetchedAt = parsed.Data.Ticket, parsed.Data.CSRF, time.Now()
	return t.ticket, t.csrf, nil
}

// ProxyTicket is the result of a vncproxy/termproxy call.
type ProxyTicket struct {
	Port     string
	Ticket   string // vncticket — websocket query auth
	Password string // one-time VNC password (vnc only)
}

// CreateProxy calls vncproxy (kind "vnc") or termproxy (kind "term") for
// the guest using the ticket session.
func (t *TicketAuth) CreateProxy(ctx context.Context, node, guestType string, vmid int, kind string) (*ProxyTicket, error) {
	ticket, csrf, err := t.Ticket(ctx)
	if err != nil {
		return nil, err
	}

	endpoint := "termproxy"
	form := url.Values{}
	if kind == "vnc" {
		endpoint = "vncproxy"
		form.Set("websocket", "1")
		form.Set("generate-password", "1")
	}
	target := fmt.Sprintf("%s/nodes/%s/%s/%d/%s", t.baseURL, url.PathEscape(node), guestType, vmid, endpoint)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader([]byte(form.Encode())))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("CSRFPreventionToken", csrf)
	req.AddCookie(&http.Cookie{Name: "PVEAuthCookie", Value: ticket})

	res, err := t.client.Do(req)
	if err != nil {
		return nil, &types.APIError{Code: "proxmox_unreachable", Message: "Could not reach Proxmox to open the console.", PVEMessage: err.Error(), Status: http.StatusBadGateway}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		return nil, &types.APIError{
			Code:       "proxmox_error",
			Message:    "Proxmox rejected the console request.",
			PVEMessage: fmt.Sprintf("%s: %s", res.Status, bytes.TrimSpace(raw)),
			Status:     http.StatusBadGateway,
		}
	}
	var parsed struct {
		Data struct {
			Port     json.Number `json:"port"`
			Ticket   string      `json:"ticket"`
			Password string      `json:"password"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil || parsed.Data.Ticket == "" {
		return nil, fmt.Errorf("console: malformed %s response", endpoint)
	}
	return &ProxyTicket{Port: parsed.Data.Port.String(), Ticket: parsed.Data.Ticket, Password: parsed.Data.Password}, nil
}
