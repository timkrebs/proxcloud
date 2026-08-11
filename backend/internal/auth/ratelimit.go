package auth

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// Login rate limiting: a small fixed window per client IP plus a global
// cap on concurrent bcrypt comparisons, so a flood can neither brute-force
// the single admin credential nor burn CPU.
const (
	loginWindow      = time.Minute
	loginMaxPerIP    = 5
	bcryptConcurrent = 4
)

// LoginLimiter is safe for concurrent use.
type LoginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
	sem      chan struct{}
	now      func() time.Time
}

// NewLoginLimiter returns a ready limiter.
func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{
		attempts: map[string][]time.Time{},
		sem:      make(chan struct{}, bcryptConcurrent),
		now:      time.Now,
	}
}

// Allow records an attempt for ip and reports whether it is within the
// window. Called before the credential check so failures and successes
// count alike (a success resets via Reset).
func (l *LoginLimiter) Allow(ip string) bool {
	now := l.now()
	cutoff := now.Add(-loginWindow)

	l.mu.Lock()
	defer l.mu.Unlock()

	kept := l.attempts[ip][:0]
	for _, t := range l.attempts[ip] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= loginMaxPerIP {
		l.attempts[ip] = kept
		return false
	}
	l.attempts[ip] = append(kept, now)

	// Opportunistic prune so idle IPs don't accumulate forever.
	if len(l.attempts) > 4096 {
		for k, ts := range l.attempts {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(l.attempts, k)
			}
		}
	}
	return true
}

// Reset clears an IP's window after a successful login.
func (l *LoginLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, ip)
}

// AcquireBcrypt bounds concurrent hash comparisons; the release func must
// be called after the comparison.
func (l *LoginLimiter) AcquireBcrypt() func() {
	l.sem <- struct{}{}
	return func() { <-l.sem }
}

// clientIP extracts the remote IP (RealIP middleware already normalized
// RemoteAddr when behind a proxy).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
