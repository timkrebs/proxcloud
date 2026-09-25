package httpserver

import (
	"bytes"
	"context"
	"fmt"
	"hash/maphash"
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
		l.rememberSession(uint64(i), "u1")
	}
	if n := len(l.sessions); n > maxValidatedSessions {
		t.Fatalf("validated-session set holds %d, cap %d", n, maxValidatedSessions)
	}
}

// TestSessionsOfOneUserShareABucket: a validated session draws from its
// user's bucket, so signing in again (each sign-in mints a session) buys an
// account no extra budget, while another user keeps a budget of their own.
func TestSessionsOfOneUserShareABucket(t *testing.T) {
	l := newAPIRateLimiter(3, time.Minute)
	for _, s := range []struct{ cookie, user string }{{"sess-a1", "alice"}, {"sess-a2", "alice"}, {"sess-b1", "bob"}} {
		l.rememberSession(maphash.String(l.seed, s.cookie), s.user)
	}
	h := rateLimit(l)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	send := func(cookie string) int {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.RemoteAddr = "192.0.2.81:1234"
		req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	var ok int
	for i := 0; i < 5; i++ {
		for _, c := range []string{"sess-a1", "sess-a2"} {
			if send(c) == http.StatusOK {
				ok++
			}
		}
	}
	if ok != 3 {
		t.Fatalf("alice's two sessions got %d requests through, want 3 (one shared bucket)", ok)
	}
	if code := send("sess-b1"); code != http.StatusOK {
		t.Fatalf("bob = %d, want 200 (own bucket)", code)
	}
}

// TestSessionMarksFollowAuthenticate: a cookie is marked only when the wrapped
// Authenticate accepts it, under the user Authenticate put in the context —
// never anything the request asserts — and a cookie it turns away loses any
// mark it had.
func TestSessionMarksFollowAuthenticate(t *testing.T) {
	l := newAPIRateLimiter(3, time.Minute)
	// Stand-in for auth.Authenticate: "good-<user>" is a live session of
	// <user>, "nouser" is accepted with an empty identity, the rest is refused.
	authenticate := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(auth.CookieName)
			switch {
			case err != nil:
				w.WriteHeader(http.StatusUnauthorized)
			case c.Value == "nouser":
				next.ServeHTTP(w, r.WithContext(auth.ContextWithIdentity(r.Context(), &auth.Identity{})))
			case strings.HasPrefix(c.Value, "good-"):
				id := &auth.Identity{UserID: strings.TrimPrefix(c.Value, "good-")}
				next.ServeHTTP(w, r.WithContext(auth.ContextWithIdentity(r.Context(), id)))
			default:
				w.WriteHeader(http.StatusUnauthorized)
			}
		})
	}
	var reached int
	h := l.withSessionMarks(authenticate)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached++
		w.WriteHeader(http.StatusOK)
	}))
	send := func(cookie string) {
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: cookie})
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	mark := func(cookie string) (string, bool) {
		m, ok := l.sessions[maphash.String(l.seed, cookie)]
		return m.user, ok
	}

	l.rememberSession(maphash.String(l.seed, "revoked"), "alice") // marked while it was live
	for _, c := range []string{"", "nouser", "forged", "revoked", "good-alice"} {
		send(c)
	}

	if reached != 2 {
		t.Fatalf("handler reached %d times, want 2 (only the accepted requests)", reached)
	}
	if user, ok := mark("good-alice"); !ok || user != "alice" {
		t.Fatalf("accepted cookie marked as (%q, %v), want (alice, true)", user, ok)
	}
	for _, c := range []string{"nouser", "forged", "revoked"} {
		if _, ok := mark(c); ok {
			t.Fatalf("cookie %q is marked; only cookies Authenticate accepts for a user may be", c)
		}
	}
}

// TestSessionMarkExpires: a mark lapses sessionMarkTTL after Authenticate last
// accepted the cookie, so a cookie revoked where the limiter cannot see it
// (sent only to public routes) stops drawing from its user's bucket.
func TestSessionMarkExpires(t *testing.T) {
	l := newAPIRateLimiter(3, time.Minute)
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }
	l.rememberSession(maphash.String(l.seed, "sess"), "alice")
	req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
	req.RemoteAddr = "192.0.2.82:1234"
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: "sess"})

	l.now = func() time.Time { return base.Add(sessionMarkTTL - time.Second) }
	if got := l.keyFor(req); got != "u:alice" {
		t.Fatalf("fresh mark keyed %q, want u:alice", got)
	}
	l.now = func() time.Time { return base.Add(sessionMarkTTL) }
	if got := l.keyFor(req); got != "ip:192.0.2.82" {
		t.Fatalf("lapsed mark keyed %q, want ip:192.0.2.82", got)
	}
	if n := len(l.sessions); n != 0 {
		t.Fatalf("lapsed mark still held (%d entries)", n)
	}
}

// TestRouterForgetsRevokedSession drives the REAL router: once a marked
// session is revoked, the first authenticated request presenting it is
// refused and drops the mark, so the dead cookie shares its IP's bucket
// instead of spending its former user's budget — while the user's live
// session keeps that budget through an IP flood.
func TestRouterForgetsRevokedSession(t *testing.T) {
	log := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	fake := storetest.New()
	sessions := auth.NewSessions(fake, false, false, time.Hour, 24*time.Hour)
	authHandler := &auth.Handler{
		Sessions: sessions, Store: fake, Hasher: auth.NewHasher(), Log: log, Limiter: auth.NewLoginLimiter(),
	}
	router := New(Deps{Cfg: &config.Config{}, Log: log, Auth: authHandler})
	ctx := context.Background()
	userID := fake.AddUser("alice@example.com", "Alice", false)
	issue := func() *http.Cookie {
		c, err := sessions.Issue(ctx, userID, httptest.NewRequest(http.MethodGet, "/", nil))
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		return c
	}
	send := func(path string, c *http.Cookie) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "192.0.2.91:1234"
		if c != nil {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	old := issue()
	if code := send("/api/auth/me", old); code != http.StatusOK {
		t.Fatalf("live session = %d, want 200", code)
	}
	if err := fake.RevokeOtherUserSessions(ctx, userID, ""); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	current := issue()
	if code := send("/api/auth/me", current); code != http.StatusOK {
		t.Fatalf("new session = %d, want 200", code)
	}
	if code := send("/api/auth/me", old); code != http.StatusUnauthorized {
		t.Fatalf("revoked session = %d, want 401", code)
	}
	for i := 0; i < apiRateLimitPerMin; i++ {
		send("/api/auth/bootstrap-status", nil)
	}
	if code := send("/api/auth/me", old); code != http.StatusTooManyRequests {
		t.Fatalf("revoked cookie after the IP flood = %d, want 429 (mark dropped, IP bucket)", code)
	}
	if code := send("/api/auth/me", current); code != http.StatusOK {
		t.Fatalf("live session after the IP flood = %d, want 200 (user bucket)", code)
	}
}

// TestRouterMarksOnlyAuthenticatedSessions drives the REAL router: a session
// draws from its user's bucket only after Authenticate has accepted it. A
// made-up cookie that reaches Authenticate is refused (401) and never marked,
// so once the shared IP bucket is exhausted it is throttled with every other
// anonymous request — while the genuinely signed-in session keeps its user's
// budget.
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
