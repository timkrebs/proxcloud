package console

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A missing/expired one-shot id is always rejected, regardless of cookie.
func TestProxyRejectsUnknownSession(t *testing.T) {
	p := &Proxy{Sessions: NewSessions(), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/console/ws/deadbeef", nil)
	req.SetPathValue("sessionId", "deadbeef")
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session = %d, want 404", rec.Code)
	}
}

// The cookie check is advisory: absent is allowed (the one-shot id is the
// credential, and a Secure cookie legitimately can't traverse dev ws://);
// a presented-but-invalid cookie is rejected before the session is claimed.
func TestProxyCookieAdvisory(t *testing.T) {
	verify := func(r *http.Request) bool {
		c, err := r.Cookie("proxcloud_session")
		if err != nil {
			return true // absent → allowed
		}
		return c.Value == "valid"
	}

	newProxy := func() (*Proxy, string) {
		s := NewSessions()
		id := s.Create(Session{Node: "pve01", GuestType: "qemu", VMID: 101, Kind: "vnc", Proxy: &ProxyTicket{Port: "5900"}})
		return &Proxy{Sessions: s, VerifyCookie: verify, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}, id
	}

	// Forged cookie → 401, and the session must NOT be consumed.
	p, id := newProxy()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/console/ws/"+id, nil)
	req.SetPathValue("sessionId", id)
	req.AddCookie(&http.Cookie{Name: "proxcloud_session", Value: "forged"})
	p.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("forged cookie = %d, want 401", rec.Code)
	}
	if _, ok := p.Sessions.Claim(id); !ok {
		t.Error("session was consumed despite the 401 — a bad cookie must not burn the id")
	}

	// No cookie → passes the gate (fails later at the PVE dial, not 401/404).
	p, id = newProxy()
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/console/ws/"+id, nil)
	req.SetPathValue("sessionId", id)
	// No Auth wired, so the dial fails with 502 — the point is it got PAST
	// the cookie gate and consumed the session.
	p.Auth = nil
	func() {
		defer func() { _ = recover() }() // nil Auth panics at dial; we only assert the gate was passed
		p.ServeHTTP(rec, req)
	}()
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusNotFound {
		t.Fatalf("no-cookie request rejected at the gate (%d) — should be allowed", rec.Code)
	}
}
