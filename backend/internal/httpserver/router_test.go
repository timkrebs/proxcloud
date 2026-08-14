package httpserver

import (
	"bytes"
	"log/slog"
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
