package httpserver

import (
	"bytes"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

// TestRedactPath proves the single-use invite token (and console session id) is
// stripped from any logged path — for BOTH the validate and accept routes — so a
// real credential never lands in the structured/access log (ADR-0013 §5.1).
func TestRedactPath(t *testing.T) {
	const token = "s3cr3t-invite-TOKEN-do-not-log"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"invite validate", "/api/auth/invitations/" + token, "/api/auth/invitations/[redacted]"},
		{"invite accept", "/api/auth/invitations/" + token + "/accept", "/api/auth/invitations/[redacted]/accept"},
		{"console ws", "/api/console/ws/" + token, "/api/console/ws/[redacted]"},
		{"unrelated path untouched", "/api/tenants/t-1/invitations", "/api/tenants/t-1/invitations"},
		{"bare invitations prefix", "/api/auth/invitations/", "/api/auth/invitations/"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := redactPath(tt.in)
			if got != tt.want {
				t.Fatalf("redactPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if strings.Contains(got, token) {
				t.Fatalf("redactPath(%q) leaked the token: %q", tt.in, got)
			}
		})
	}
}

// TestSecurityHeaders is the H1 regression: every backend response carries the
// hardening headers (nosniff, DENY framing, a locked-down CSP with
// frame-ancestors 'none', HSTS) so the directly-reachable API/WS surface can't
// be framed, sniffed, or downgraded.
func TestSecurityHeaders(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fake := storetest.New()
	authHandler := &auth.Handler{
		Sessions: auth.NewSessions(fake, false, false, time.Hour, 24*time.Hour),
		Store:    fake, Hasher: auth.NewHasher(), Log: log, Limiter: auth.NewLoginLimiter(),
	}
	router := New(Deps{Cfg: &config.Config{}, Log: log, Auth: authHandler})

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	h := rec.Header()
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := h.Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q, want frame-ancestors 'none'", csp)
	}
	if h.Get("Strict-Transport-Security") == "" {
		t.Error("missing Strict-Transport-Security")
	}
}

// TestHostAllowlist is the M1 regression: with an allowlist configured, a
// request with an unknown Host is rejected 421; an allowed Host passes.
func TestHostAllowlist(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	router := New(Deps{
		Cfg: &config.Config{AllowedHosts: []string{"portal.test"}},
		Log: log,
	})

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = "attacker.example"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("unknown Host = %d, want 421", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = "portal.test:443" // host:port still matches the bare host
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code == http.StatusMisdirectedRequest {
		t.Fatalf("allowed Host wrongly rejected 421")
	}
}

// TestTrustedProxyHeaders is the H2 regression: forwarded headers are honored
// only from a configured trusted-proxy peer; from any other (direct) peer they
// are stripped, so a client reaching the origin directly cannot spoof the
// rate-limit key / audit IP or downgrade the Secure cookie via X-Forwarded-Proto.
func TestTrustedProxyHeaders(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("10.0.0.0/8")
	cfg := &config.Config{TrustProxyHeaders: true, TrustedProxies: []*net.IPNet{cidr}}

	var remote, xfp, xff string
	h := trustedProxyHeaders(cfg)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		remote, xfp, xff = r.RemoteAddr, r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Forwarded-For")
	}))

	// Trusted peer (10.x): recover the real client IP; keep X-Forwarded-Proto.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.1.2.3:5555"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Forwarded-Proto", "https")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if remote != "203.0.113.9" {
		t.Fatalf("trusted RemoteAddr = %q, want recovered client 203.0.113.9", remote)
	}
	if xfp != "https" {
		t.Fatalf("trusted X-Forwarded-Proto = %q, want https", xfp)
	}

	// Untrusted/direct peer: strip forwarded headers, leave RemoteAddr alone.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "198.51.100.7:4444"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Forwarded-Proto", "http")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if remote != "198.51.100.7:4444" {
		t.Fatalf("untrusted RemoteAddr = %q, want unchanged (no spoofed client IP)", remote)
	}
	if xff != "" || xfp != "" {
		t.Fatalf("untrusted forwarded headers not stripped: XFF=%q XFP=%q", xff, xfp)
	}
}

// TestOriginCheckFailsClosedWithoutOrigin is the L1 regression: a cookie-
// authenticated state-changing request with NO Origin/Referer is rejected 403
// (CSRF must not rest on SameSite alone), while a valid Origin passes and a
// header-less request WITHOUT the cookie still passes (no ambient credential).
func TestOriginCheckFailsClosedWithoutOrigin(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fake := storetest.New()
	authHandler := &auth.Handler{
		Sessions: auth.NewSessions(fake, false, false, time.Hour, 24*time.Hour),
		Store:    fake, Hasher: auth.NewHasher(), Log: log, Limiter: auth.NewLoginLimiter(),
	}
	router := New(Deps{Cfg: &config.Config{}, Log: log, Auth: authHandler})

	// Cookie + no Origin/Referer → 403 (before routing/auth).
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "whatever"})
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cookie POST without Origin = %d, want 403 (fail-closed CSRF)", rec.Code)
	}

	// Cookie + valid Origin → passes originCheck (reaches auth, not 403).
	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "whatever"})
	req.Header.Set("Origin", "http://localhost")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("cookie POST with valid Origin wrongly 403'd")
	}

	// No cookie + no Origin → passes (non-browser automation, no ambient cred).
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("no-cookie header-less POST wrongly 403'd")
	}
}

// TestLimitBodyRejectsOversize is the H4 regression: an over-declared request
// body is rejected with 413 by the middleware BEFORE any handler decodes/hashes
// it, so a public endpoint cannot be used to OOM the backend. A normal small
// body is not affected.
func TestLimitBodyRejectsOversize(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fake := storetest.New()
	authHandler := &auth.Handler{
		Sessions: auth.NewSessions(fake, false, false, time.Hour, 24*time.Hour),
		Store:    fake, Hasher: auth.NewHasher(), Log: log, Limiter: auth.NewLoginLimiter(),
	}
	router := New(Deps{Cfg: &config.Config{}, Log: log, Auth: authHandler})

	// Oversize body → 413, never reaching the login handler.
	big := strings.Repeat("a", maxRequestBodyBytes+1)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize body = %d, want 413 (%s)", rec.Code, rec.Body.String())
	}

	// A small (even malformed) body is NOT rejected as 413 — it reaches the handler.
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"bad"`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code == http.StatusRequestEntityTooLarge {
		t.Fatalf("small body wrongly rejected as 413")
	}
}

// TestAccessLogRedactsInviteToken drives the real router end-to-end for both the
// validate and accept invite routes and asserts the raw token never appears in
// the emitted access-log line (while the redacted marker does). This is the
// regression guard for the security BLOCK.
func TestAccessLogRedactsInviteToken(t *testing.T) {
	const token = "known-token-9f8e7d6c-never-logged"

	fake := storetest.New()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	authHandler := &auth.Handler{
		Sessions: auth.NewSessions(fake, false, false, time.Hour, 24*time.Hour),
		Store:    fake,
		Hasher:   auth.NewHasher(),
		Log:      log,
		Limiter:  auth.NewLoginLimiter(),
	}
	router := New(Deps{
		Cfg:  &config.Config{},
		Log:  log,
		Auth: authHandler,
	})

	cases := []struct {
		name    string
		method  string
		target  string
		body    string
		wantLog string
	}{
		{"validate", http.MethodGet, "/api/auth/invitations/" + token, "", "/api/auth/invitations/[redacted]"},
		{"accept", http.MethodPost, "/api/auth/invitations/" + token + "/accept", `{}`, "/api/auth/invitations/[redacted]/accept"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf.Reset()
			var bodyReader *strings.Reader
			if tc.body != "" {
				bodyReader = strings.NewReader(tc.body)
			} else {
				bodyReader = strings.NewReader("")
			}
			req := httptest.NewRequest(tc.method, tc.target, bodyReader)
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)

			logged := buf.String()
			if strings.Contains(logged, token) {
				t.Fatalf("access log leaked the raw invite token:\n%s", logged)
			}
			if !strings.Contains(logged, tc.wantLog) {
				t.Fatalf("access log missing redacted path %q:\n%s", tc.wantLog, logged)
			}
		})
	}
}

// TestForwardedIPValidation is the ADR-0034 topology regression (H-1.1/2):
// a forwarded value from a TRUSTED peer is honored only when it parses as an
// IP; garbage/non-IP values fall back to the peer's own address, so RemoteAddr
// never carries an attacker-chosen arbitrary string into rate-limit keys,
// audit rows, or session IP columns.
//
// Contract note (the spoofed-but-valid case): two requests from the SAME
// trusted peer carrying DIFFERENT valid X-Real-IP values yield different keys
// — the backend cannot distinguish a proxy-set header from a client-set one.
// That is safe ONLY because the deployment contract (ADR-0034) makes the edge
// OVERWRITE X-Real-IP unconditionally (`header_up X-Real-IP {client_ip}` in
// deploy/host/*/caddy), so by the time a trusted peer forwards the header its
// value is the connection's real client IP, not client input. This test pins
// the backend half of the contract: validation + trusted-peer-only reads.
func TestForwardedIPValidation(t *testing.T) {
	_, cidr, _ := net.ParseCIDR("10.0.0.0/8")
	cfg := &config.Config{TrustProxyHeaders: true, TrustedProxies: []*net.IPNet{cidr}}

	var remote string
	h := trustedProxyHeaders(cfg)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		remote = r.RemoteAddr
	}))
	send := func(xRealIP, xff string) string {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.1.2.3:5555"
		if xRealIP != "" {
			req.Header.Set("X-Real-IP", xRealIP)
		}
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
		return remote
	}

	tests := []struct {
		name, xRealIP, xff, want string
	}{
		{"valid X-Real-IP honored (edge-overwrite contract)", "203.0.113.9", "", "203.0.113.9"},
		{"garbage X-Real-IP falls back to peer", "not-an-ip", "", "10.1.2.3:5555"},
		{"injection payload falls back to peer", "1.2.3.4; DROP TABLE", "", "10.1.2.3:5555"},
		{"garbage X-Real-IP, valid XFF → XFF first hop", "zzz", "198.51.100.7, 10.1.2.3", "198.51.100.7"},
		{"garbage in both falls back to peer", "zzz", "also-garbage, 10.1.2.3", "10.1.2.3:5555"},
		{"IPv6 X-Real-IP parses", "2001:db8::7", "", "2001:db8::7"},
		{"no headers → peer unchanged", "", "", "10.1.2.3:5555"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := send(tt.xRealIP, tt.xff); got != tt.want {
				t.Fatalf("RemoteAddr = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRateLimitSessionAndIPBuckets is the ADR-0034 keying regression (H-1.3):
// in the tunneled topology every user shares the proxy's peer IP, so an
// unauthenticated flood must land in the IP bucket while cookie-bearing
// requests ride their own per-session bucket — the flood cannot 429 signed-in
// users. The exempt probe path never counts.
func TestRateLimitSessionAndIPBuckets(t *testing.T) {
	limiter := newAPIRateLimiter(3, time.Minute)
	h := rateLimit(limiter, "/api/health")(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	send := func(path, cookie string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "192.0.2.50:1234" // ONE shared peer IP for everyone
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// Unauthenticated flood from the shared IP exhausts the IP bucket…
	for i := 0; i < 3; i++ {
		if code := send("/api/x", ""); code != http.StatusOK {
			t.Fatalf("unauth request %d = %d, want 200", i, code)
		}
	}
	if code := send("/api/x", ""); code != http.StatusTooManyRequests {
		t.Fatalf("unauth flood overflow = %d, want 429", code)
	}
	// …but a cookie-bearing request from the SAME IP has its own bucket.
	if code := send("/api/x", "session-token-alice"); code != http.StatusOK {
		t.Fatalf("cookie-bearing request during unauth flood = %d, want 200 (own bucket)", code)
	}
	// A different session is a different bucket too.
	if code := send("/api/x", "session-token-bob"); code != http.StatusOK {
		t.Fatalf("second session during flood = %d, want 200", code)
	}
	// One session exhausting ITS bucket does not spill onto another.
	for i := 0; i < 2; i++ {
		send("/api/x", "session-token-alice")
	}
	if code := send("/api/x", "session-token-alice"); code != http.StatusTooManyRequests {
		t.Fatalf("alice past her budget = %d, want 429", code)
	}
	if code := send("/api/x", "session-token-bob"); code != http.StatusOK {
		t.Fatalf("bob throttled by alice's flood = %d, want 200", code)
	}
	// The probe path is exempt no matter what.
	if code := send("/api/health", ""); code != http.StatusOK {
		t.Fatalf("/api/health during flood = %d, want 200 (probe exempt)", code)
	}
}

// TestRouterHealthNeverRateLimited drives the REAL router: the global limiter
// throttles ordinary routes but /api/health and /api/v1/version stay exempt,
// so the Docker HEALTHCHECK and deploy gates can never be starved by a flood.
func TestRouterHealthNeverRateLimited(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fake := storetest.New()
	authHandler := &auth.Handler{
		Sessions: auth.NewSessions(fake, false, false, time.Hour, 24*time.Hour),
		Store:    fake, Hasher: auth.NewHasher(), Log: log, Limiter: auth.NewLoginLimiter(),
	}
	router := New(Deps{Cfg: &config.Config{}, Log: log, Auth: authHandler})

	send := func(path string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "192.0.2.60:1234"
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}
	// Exhaust the shared-IP budget on an ordinary route.
	throttled := false
	for i := 0; i < apiRateLimitPerMin+1; i++ {
		if send("/api/auth/bootstrap-status") == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Fatal("ordinary route was never rate-limited — global limiter not wired")
	}
	if code := send("/api/health"); code != http.StatusOK {
		t.Fatalf("/api/health during flood = %d, want 200", code)
	}
	if code := send("/api/v1/version"); code != http.StatusOK {
		t.Fatalf("/api/v1/version during flood = %d, want 200", code)
	}
}

// TestHostAllowlistLoopbackAlwaysAllowed is the L-a regression: with an
// allowlist configured, loopback Hosts (the Docker HEALTHCHECK's 127.0.0.1,
// ::1, localhost — with or without port) always pass; only foreign hosts 421.
func TestHostAllowlistLoopbackAlwaysAllowed(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	router := New(Deps{
		Cfg: &config.Config{AllowedHosts: []string{"portal.test"}},
		Log: log,
	})
	for _, host := range []string{"127.0.0.1", "127.0.0.1:8080", "localhost", "localhost:8090", "[::1]", "[::1]:8080"} {
		req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
		req.Host = host
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusMisdirectedRequest {
			t.Fatalf("loopback Host %q rejected 421 — on-box probes must always pass", host)
		}
	}
	// The allowlist still bites for anything else.
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = "attacker.example"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("foreign Host = %d, want 421", rec.Code)
	}
}

// TestRejectionsCarrySecurityHeadersAndLog pins the middleware ORDER fix: a
// 421 (host allowlist) and a 429 (rate limit) both carry the hardening headers
// and appear in the access log — the rejection producers run inside
// securityHeaders, accessLog, and Recoverer.
func TestRejectionsCarrySecurityHeadersAndLog(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	router := New(Deps{Cfg: &config.Config{AllowedHosts: []string{"portal.test"}}, Log: log})

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Host = "attacker.example"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("code = %d, want 421", rec.Code)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("421 rejection lacks security headers — securityHeaders must wrap hostAllowlist")
	}
	if !strings.Contains(buf.String(), "421") {
		t.Fatalf("421 rejection missing from the access log:\n%s", buf.String())
	}
}
