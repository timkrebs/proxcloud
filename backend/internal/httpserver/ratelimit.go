package httpserver

import (
	"hash/maphash"
	"net"
	"net/http"
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

const (
	// maxLimiterBuckets hard-caps the bucket map: memory stays bounded however
	// many distinct clients (or forged keys) arrive.
	maxLimiterBuckets = 16_384
	// maxValidatedSessions bounds the set of session cookies Authenticate has
	// accepted. Only a real sign-in adds to it, so it holds live sessions.
	maxValidatedSessions = 8_192
	// limiterPruneEvery is the minimum gap between full-map prunes of expired
	// buckets, so a key flood can never force a scan per request.
	limiterPruneEvery = 10 * time.Second
	// sessionMarkTTL bounds how long a cookie keeps drawing from its user's
	// bucket after Authenticate last accepted it. The UI polls every few
	// seconds, so open tabs never lapse; a cookie revoked where the limiter
	// cannot see it stops spending its user's budget within this window even
	// if it is only ever sent to public routes.
	sessionMarkTTL = 15 * time.Minute
)

// apiRateLimiter is a per-key fixed-window request limiter, safe for concurrent
// use. Every operation is O(1) except a prune of expired buckets, which runs
// at most once per limiterPruneEvery and only when the map is full.
//
// Keying (ADR-0034): a request whose session cookie Authenticate has recently
// accepted draws from its USER's bucket — every session of one account shares
// it, so minting more sessions buys no more budget; every other request —
// cookie-less, or carrying a cookie the server has not validated — draws from
// its client IP's bucket (IPv6 by /64). A made-up cookie value therefore buys
// nothing: it shares its IP's budget like any anonymous request. The split
// still matters where signed-in users share one client IP (a NAT) with
// unauthenticated traffic.
type apiRateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*rlBucket
	sessions  map[uint64]sessionMark // keyed by the hash of a cookie Authenticate accepted
	limit     int
	window    time.Duration
	now       func() time.Time
	seed      maphash.Seed
	lastPrune int64
	prunes    int // full-map prunes performed (test visibility)
}

// rlBucket is one key's current window.
type rlBucket struct {
	start int64 // window start, unix nanoseconds
	count int
}

// sessionMark is what the limiter knows about a validated session cookie:
// whose it is, and when Authenticate last accepted it.
type sessionMark struct {
	user string
	seen int64 // unix nanoseconds
}

func newAPIRateLimiter(limit int, window time.Duration) *apiRateLimiter {
	return &apiRateLimiter{
		buckets:  map[string]*rlBucket{},
		sessions: map[uint64]sessionMark{},
		limit:    limit,
		window:   window,
		now:      time.Now,
		// Per-process random seed: cookie values cannot be offline-crafted to
		// collide with (and thereby share) another client's bucket.
		seed: maphash.MakeSeed(),
	}
}

// keyFor derives the limiter key for one request: "u:<user id>" for a session
// cookie Authenticate accepted within sessionMarkTTL, else "ip:<client ip or
// /64>".
func (a *apiRateLimiter) keyFor(r *http.Request) string {
	if c, err := r.Cookie(auth.CookieName); err == nil && c.Value != "" {
		h := maphash.String(a.seed, c.Value)
		now := a.now().UnixNano()
		a.mu.Lock()
		m, marked := a.sessions[h]
		if marked && now-m.seen >= sessionMarkTTL.Nanoseconds() {
			delete(a.sessions, h)
			marked = false
		}
		a.mu.Unlock()
		if marked {
			return "u:" + m.user
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return "ip:" + auth.LimiterIPKey(host)
}

// allow counts one request against key's current window. The window is
// fixed, so a client can burst up to twice the limit across a window edge —
// acceptable for a coarse flood guard.
func (a *apiRateLimiter) allow(key string) bool {
	now := a.now().UnixNano()
	a.mu.Lock()
	defer a.mu.Unlock()

	b, ok := a.buckets[key]
	switch {
	case !ok:
		a.makeRoomLocked(now)
		b = &rlBucket{start: now}
		a.buckets[key] = b
	case now-b.start >= a.window.Nanoseconds():
		b.start, b.count = now, 0
	}
	if b.count >= a.limit {
		return false
	}
	b.count++
	return true
}

// makeRoomLocked keeps the map under maxLimiterBuckets without a per-request
// scan: expired buckets are swept at most once per limiterPruneEvery, and if
// the map is still full, one arbitrary bucket is evicted (map iteration order
// is randomized, so this is O(1)). Evicting a bucket only forgets that
// client's recent count — it can never grant more than a fresh window.
func (a *apiRateLimiter) makeRoomLocked(now int64) {
	if len(a.buckets) < maxLimiterBuckets {
		return
	}
	if now-a.lastPrune >= limiterPruneEvery.Nanoseconds() {
		a.lastPrune = now
		a.prunes++
		for k, b := range a.buckets {
			if now-b.start >= a.window.Nanoseconds() {
				delete(a.buckets, k)
			}
		}
	}
	for k := range a.buckets {
		if len(a.buckets) < maxLimiterBuckets {
			break
		}
		delete(a.buckets, k)
	}
}

// rememberSession records that the session-cookie hash h, just accepted by
// Authenticate, belongs to userID. The set is bounded; when full, an arbitrary
// entry is dropped (O(1)) — that session merely shares its IP's bucket until
// its next authenticated request re-records it.
func (a *apiRateLimiter) rememberSession(h uint64, userID string) {
	now := a.now().UnixNano()
	a.mu.Lock()
	defer a.mu.Unlock()
	if _, ok := a.sessions[h]; !ok {
		for k := range a.sessions {
			if len(a.sessions) < maxValidatedSessions {
				break
			}
			delete(a.sessions, k)
		}
	}
	a.sessions[h] = sessionMark{user: userID, seen: now}
}

func (a *apiRateLimiter) forgetSession(h uint64) {
	a.mu.Lock()
	delete(a.sessions, h)
	a.mu.Unlock()
}

// withSessionMarks wraps auth.Authenticate so the limiter learns from its
// verdict: a cookie it accepts is marked as the context identity's session and
// draws from that user's bucket from then on; a cookie it turns away —
// revoked, expired, never valid — loses any mark at once. Wrapping, rather
// than a separate middleware mounted after Authenticate, means the two can
// never be reordered. Both read the first proxcloud_session cookie, so a
// request carrying two cannot validate one and mark the other. A session's
// first request after sign-in still counts against its IP's bucket.
func (a *apiRateLimiter) withSessionMarks(authenticate func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		plain := authenticate(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := r.Cookie(auth.CookieName)
			if err != nil || c.Value == "" {
				plain.ServeHTTP(w, r)
				return
			}
			h := maphash.String(a.seed, c.Value)
			accepted := false
			authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				accepted = true
				if id, ok := auth.IdentityFrom(r.Context()); ok && id != nil && id.UserID != "" {
					a.rememberSession(h, id.UserID)
				}
				next.ServeHTTP(w, r)
			})).ServeHTTP(w, r)
			if !accepted {
				a.forgetSession(h)
			}
		})
	}
}

// rateLimit is the global throttle middleware. Only the exempt paths (on-box
// probes: /api/health, /api/v1/version) bypass it. The streaming routes are NOT
// exempt — an SSE/console open counts once against its bucket, so a flood of
// stream-opens cannot exhaust the DB pool via per-open session lookups; they
// remain exempt from the body cap and request timeout.
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
