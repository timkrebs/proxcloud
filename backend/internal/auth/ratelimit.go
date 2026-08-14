package auth

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Login rate limiting: a small fixed window per client IP, an IP-independent
// per-ACCOUNT lockout (so a distributed attack rotating source IPs still can't
// brute-force one account), plus a global cap on concurrent bcrypt comparisons.
const (
	loginWindow      = time.Minute
	loginMaxPerIP    = 5
	bcryptConcurrent = 4

	// Per-account lockout: after this many consecutive failures the account is
	// locked with exponential backoff (base doubling per extra failure), capped.
	accountFailThreshold = 5
	accountLockBase      = time.Minute
	accountLockMax       = 15 * time.Minute
)

// accountState tracks consecutive failed logins for one account and, once the
// threshold is crossed, the time the lockout expires.
type accountState struct {
	failures    int
	lockedUntil time.Time
}

// LoginLimiter is safe for concurrent use.
type LoginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time   // per-IP fixed window
	accounts map[string]*accountState // per-account lockout (IP-independent)
	sem      chan struct{}
	now      func() time.Time
}

// NewLoginLimiter returns a ready limiter.
func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{
		attempts: map[string][]time.Time{},
		accounts: map[string]*accountState{},
		sem:      make(chan struct{}, bcryptConcurrent),
		now:      time.Now,
	}
}

// accountKey normalizes an account identifier (email) for lockout bookkeeping.
func accountKey(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// AllowAccount reports whether the account is NOT currently locked out. Check it
// before spending a credential verification so a locked account short-circuits.
func (l *LoginLimiter) AllowAccount(email string) bool {
	key := accountKey(email)
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.accounts[key]
	return s == nil || !l.now().Before(s.lockedUntil)
}

// RecordFailure counts a failed login for the account and, past the threshold,
// applies exponential backoff. Keyed by account, so rotating source IPs does not
// evade it.
func (l *LoginLimiter) RecordFailure(email string) {
	key := accountKey(email)
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.accounts[key]
	if s == nil {
		s = &accountState{}
		l.accounts[key] = s
	}
	s.failures++
	if s.failures >= accountFailThreshold {
		backoff := accountLockBase << (s.failures - accountFailThreshold)
		if backoff <= 0 || backoff > accountLockMax {
			backoff = accountLockMax
		}
		s.lockedUntil = l.now().Add(backoff)
	}
	// Opportunistic prune of expired, unlocked accounts.
	if len(l.accounts) > 4096 {
		for k, st := range l.accounts {
			if st.failures < accountFailThreshold && l.now().Sub(st.lockedUntil) > loginWindow {
				delete(l.accounts, k)
			}
		}
	}
}

// ResetAccount clears an account's failures/lockout after a successful login.
func (l *LoginLimiter) ResetAccount(email string) {
	key := accountKey(email)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.accounts, key)
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

// clientIP extracts the remote IP. The trustedProxyHeaders middleware has
// already set RemoteAddr to the real client IP for requests from a trusted
// proxy, and left it as the direct peer otherwise — so this is never a spoofed
// forwarded value.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
