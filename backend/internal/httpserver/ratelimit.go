package httpserver

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

// apiRateLimitPerMin is the coarse global request budget per client, per minute.
// It bounds bursts so one client cannot flood the backend / Proxmox / DB, while
// staying far above real interactive use (the live UI uses one SSE stream, not
// polling). Auth endpoints keep their own stricter per-IP + per-account limits.
const apiRateLimitPerMin = 600

// apiRateLimiter is a per-key fixed-window request limiter, safe for concurrent
// use. The key is the real client IP (the trustedProxyHeaders middleware has
// already resolved RemoteAddr, so it cannot be spoofed via a forwarded header).
type apiRateLimiter struct {
	mu     sync.Mutex
	hits   map[string][]int64
	limit  int
	window time.Duration
	now    func() time.Time
}

func newAPIRateLimiter(limit int, window time.Duration) *apiRateLimiter {
	return &apiRateLimiter{hits: map[string][]int64{}, limit: limit, window: window, now: time.Now}
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

// rateLimit is the global throttle middleware. Streaming routes (SSE, console
// WS) are exempt — they are one long-lived request each and carry their own
// per-user connection caps.
func rateLimit(limiter *apiRateLimiter, exempt ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			for _, p := range exempt {
				if r.URL.Path == p || (strings.HasSuffix(p, "/") && strings.HasPrefix(r.URL.Path, p)) {
					next.ServeHTTP(w, r)
					return
				}
			}
			if !limiter.allow(rateKey(r)) {
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

// rateKey is the limiter key: the client IP (already trusted-proxy-resolved).
func rateKey(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
