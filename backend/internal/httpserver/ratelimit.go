package httpserver

import (
	"hash/maphash"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
)

// apiRateLimitPerMin is the coarse global request budget per client, per minute.
// It bounds bursts so one client cannot flood the backend / Proxmox / DB, while
// staying far above real interactive use (the live UI uses one SSE stream, not
// polling). Auth endpoints keep their own stricter per-IP + per-account limits.
const apiRateLimitPerMin = 600

// apiRateLimiter is a per-key fixed-window request limiter, safe for concurrent
// use.
//
// Keying (ADR-0034): a request carrying the proxcloud_session cookie is keyed
// on a fast seeded hash of the cookie VALUE — no DB lookup, and the key is a
// fixed-size digest so an attacker minting random cookies cannot allocate
// arbitrary-length keys. A cookie-less request is keyed on the client IP
// (already trusted-proxy-resolved). This matters in the tunneled topology
// (browser → Cloudflare → cloudflared → Caddy → backend), where every user can
// share one upstream peer IP: without the session split, ~10 rps of
// unauthenticated junk would 429 every signed-in user at once.
type apiRateLimiter struct {
	mu     sync.Mutex
	hits   map[string][]int64
	limit  int
	window time.Duration
	now    func() time.Time
	seed   maphash.Seed
}

func newAPIRateLimiter(limit int, window time.Duration) *apiRateLimiter {
	return &apiRateLimiter{
		hits:   map[string][]int64{},
		limit:  limit,
		window: window,
		now:    time.Now,
		// Per-process random seed: cookie values cannot be offline-crafted to
		// collide with (and thereby exhaust) another client's bucket.
		seed: maphash.MakeSeed(),
	}
}

// keyFor derives the limiter key for one request: "s:<hash>" for a request
// carrying the session cookie (any value — validity is irrelevant, the point is
// a stable per-client bucket without a DB lookup), else "ip:<client-ip>".
func (a *apiRateLimiter) keyFor(r *http.Request) string {
	if c, err := r.Cookie(auth.CookieName); err == nil && c.Value != "" {
		return "s:" + strconv.FormatUint(maphash.String(a.seed, c.Value), 16)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + host
}

func (a *apiRateLimiter) allow(key string) bool {
	now := a.now().UnixNano()
	cutoff := now - a.window.Nanoseconds()

	a.mu.Lock()
	defer a.mu.Unlock()

	kept := a.hits[key][:0]
	for _, t := range a.hits[key] {
		if t > cutoff {
			kept = append(kept, t)
		}
	}
	if len(kept) >= a.limit {
		a.hits[key] = kept
		return false
	}
	a.hits[key] = append(kept, now)

	if len(a.hits) > 8192 { // opportunistic prune of idle keys
		for k, ts := range a.hits {
			if len(ts) == 0 || ts[len(ts)-1] <= cutoff {
				delete(a.hits, k)
			}
		}
	}
	return true
}

// rateLimit is the global throttle middleware. Only the exempt paths (on-box
// probes: /api/health, /api/v1/version) bypass it. The streaming routes are NOT
// exempt — an SSE/console open counts once against its bucket, so a flood of
// stream-opens with random cookies cannot exhaust the DB pool via per-open
// session lookups; they remain exempt from the body cap and request timeout.
func rateLimit(limiter *apiRateLimiter, exempt ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range exempt {
				if r.URL.Path == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(r.URL.Path, p)) {
					next.ServeHTTP(w, r)
					return
				}
			}
			if !limiter.allow(limiter.keyFor(r)) {
				WriteError(w, &types.APIError{
					Code:    "rate_limited",
					Message: "Too many requests — slow down.",
					Status:  http.StatusTooManyRequests,
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
