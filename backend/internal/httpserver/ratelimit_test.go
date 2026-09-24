package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

// TestAPIRateLimiter is the H3 regression: a client is allowed up to `limit`
// requests within the window and blocked beyond it, and once the window has
// elapsed it starts a fresh one.
func TestAPIRateLimiter(t *testing.T) {
	l := newAPIRateLimiter(3, time.Minute)
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }

	for i := 0; i < 3; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("request %d blocked within budget", i)
		}
	}
	if l.allow("1.2.3.4") {
		t.Fatal("4th request within the window was not rate-limited")
	}
	// A different client has its own budget.
	if !l.allow("5.6.7.8") {
		t.Fatal("unrelated client wrongly throttled")
	}
	// After the window elapses, the first client is allowed again.
	l.now = func() time.Time { return base.Add(time.Minute + time.Second) }
	if !l.allow("1.2.3.4") {
		t.Fatal("request not allowed after the window elapsed")
	}
}

// TestRandomCookiesShareIPBucket replays the security review's proof of
// concept: every made-up session-cookie value used to mint its own bucket, so
// 60,000 requests from one IP drew zero 429s and grew the map by 60,000 keys.
// A cookie the server never validated must share its IP's bucket.
func TestRandomCookiesShareIPBucket(t *testing.T) {
	l := newAPIRateLimiter(3, time.Minute)
	h := rateLimit(l)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	var ok, limited int
	for i := 0; i < 10_000; i++ {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.RemoteAddr = "192.0.2.80:1234"
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: fmt.Sprintf("forged-%d", i)})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		switch rec.Code {
		case http.StatusOK:
			ok++
		case http.StatusTooManyRequests:
			limited++
		}
	}
	if ok != 3 || limited != 10_000-3 {
		t.Fatalf("forged cookies: %d allowed / %d limited, want 3 / %d", ok, limited, 10_000-3)
	}
	if n := len(l.buckets); n != 1 {
		t.Fatalf("limiter holds %d buckets, want 1 (the shared IP bucket)", n)
	}
}

// TestLimiterStaysBounded: however many distinct clients arrive, the bucket
// map never exceeds its cap, and the full-map prune runs at most once per
// prune interval instead of on every request past the old 8192-key mark.
func TestLimiterStaysBounded(t *testing.T) {
	l := newAPIRateLimiter(1, time.Minute)
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }

	for i := 0; i < 3*maxLimiterBuckets; i++ {
		l.allow(fmt.Sprintf("ip:10.%d.%d.%d", i>>16&0xff, i>>8&0xff, i&0xff))
		if n := len(l.buckets); n > maxLimiterBuckets {
			t.Fatalf("after %d clients the map holds %d buckets, cap %d", i+1, n, maxLimiterBuckets)
		}
	}
	if l.prunes != 1 {
		t.Fatalf("%d full-map prunes on a frozen clock, want exactly 1 (amortized)", l.prunes)
	}

	// Once the window and the prune interval have passed, the next new client
	// triggers a sweep that clears the expired buckets.
	l.now = func() time.Time { return base.Add(2 * time.Minute) }
	l.allow("ip:192.0.2.1")
	if n := len(l.buckets); n != 1 {
		t.Fatalf("after expiry the map holds %d buckets, want 1", n)
	}

	for i := 0; i < 2*maxValidatedSessions; i++ {
		l.rememberSession(uint64(i))
	}
	if n := len(l.sessions); n > maxValidatedSessions {
		t.Fatalf("validated-session set holds %d, cap %d", n, maxValidatedSessions)
	}
}

// TestRouterMarksOnlyAuthenticatedSessions drives the REAL router: a session
// earns its own bucket only after Authenticate has accepted it. A made-up
// cookie that reaches Authenticate is refused (401) and never marked, so once
// the shared IP bucket is exhausted it is throttled with every other anonymous
// request — while the genuinely signed-in session keeps its own budget.
func TestRouterMarksOnlyAuthenticatedSessions(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fake := storetest.New()
	sessions := auth.NewSessions(fake, false, false, time.Hour, 24*time.Hour)
	authHandler := &auth.Handler{
		Sessions: sessions, Store: fake, Hasher: auth.NewHasher(), Log: log, Limiter: auth.NewLoginLimiter(),
	}
	router := New(Deps{Cfg: &config.Config{}, Log: log, Auth: authHandler})

	userID := fake.AddUser("alice@example.com", "Alice", false)
	real, err := sessions.Issue(context.Background(), userID, httptest.NewRequest(http.MethodGet, "/", nil))
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	forged := &http.Cookie{Name: auth.CookieName, Value: "forged-session-value"}
	send := func(path string, c *http.Cookie) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "192.0.2.90:1234" // everyone shares one IP
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := send("/api/auth/me", real); code != http.StatusOK {
		t.Fatalf("signed-in /api/auth/me = %d, want 200", code)
	}
	if code := send("/api/auth/me", forged); code != http.StatusUnauthorized {
		t.Fatalf("forged-cookie /api/auth/me = %d, want 401", code)
	}
	// Exhaust the shared IP bucket with anonymous traffic.
	for i := 0; i < apiRateLimitPerMin; i++ {
		send("/api/auth/bootstrap-status", nil)
	}
	if code := send("/api/auth/me", forged); code != http.StatusTooManyRequests {
		t.Fatalf("forged cookie after the IP flood = %d, want 429 (never marked)", code)
	}
	if code := send("/api/auth/me", real); code != http.StatusOK {
		t.Fatalf("signed-in session after the IP flood = %d, want 200 (own bucket)", code)
	}
}
