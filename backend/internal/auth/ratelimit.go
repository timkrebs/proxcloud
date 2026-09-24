package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Login rate limiting: a small fixed window per client IP, an IP-independent
// per-ACCOUNT lockout (so a distributed attack rotating source IPs still can't
// brute-force one account), an IP- and password-independent per-account
// SECOND-FACTOR counter (so a known-password attacker cannot grind TOTP), plus
// a global cap on concurrent password-hash comparisons.
//
// All state is in-memory per instance — an ACCEPTED residual (ADR-0034 /
// docs/security/audit-2026-08-14.md): a multi-instance deployment shares
// nothing, so each instance enforces the bounds independently. A DB-backed
// lockout is tracked as an open item.
const (
	loginWindow      = time.Minute
	loginMaxPerIP    = 5
	bcryptConcurrent = 4

	// Per-account lockout: after this many failures within the decay window the
	// account is locked with exponential backoff (base doubling per extra
	// failure), capped at accountLockMax — never longer, never permanent.
	accountFailThreshold = 5
	accountLockBase      = time.Minute
	accountLockMax       = 15 * time.Minute
	// accountFailDecay: a failure streak decays — the counter resets once this
	// much time passes since the LAST failure, so no account accumulates
	// failures forever.
	accountFailDecay = 15 * time.Minute

	// Second-factor (TOTP/recovery-code) counter, keyed per account (user id)
	// and NOT reset by a correct password: ~10 consecutive failures lock the
	// 2FA step (not the account) for a bounded window. Without this, a
	// known-password attacker re-runs Login (which legitimately clears the
	// password lockout) to mint fresh challenges and brute-forces TOTP at
	// Argon2 speed.
	secondFactorFailThreshold = 10
	secondFactorLock          = 15 * time.Minute
	secondFactorFailDecay     = 15 * time.Minute

	// maxEmailBytes: RFC 5321's address ceiling. Longer inputs are rejected by
	// the handler BEFORE any limiter map is touched, so the account maps never
	// hash attacker-sized inputs (keys are fixed-size SHA-256 anyway).
	maxEmailBytes = 254

	// maxLimiterEntries hard-caps each limiter map. At the cap, the
	// oldest-expiring entry is evicted to admit the new one — bounded memory
	// under a distinct-key flood, at worst forgetting the stalest offender.
	maxLimiterEntries = 10_000
)

// limiterState tracks a decaying failure streak for one key and, once the
// threshold is crossed, the time the lock expires.
type limiterState struct {
	failures    int
	lastFailure time.Time
	lockedUntil time.Time
}

// expiresAt is the instant this entry stops mattering (used for prune +
// oldest-expiring eviction): the later of lock expiry and failure decay.
func (s *limiterState) expiresAt(decay time.Duration) time.Time {
	e := s.lastFailure.Add(decay)
	if s.lockedUntil.After(e) {
		return s.lockedUntil
	}
	return e
}

// LoginLimiter is safe for concurrent use.
type LoginLimiter struct {
	mu           sync.Mutex
	attempts     map[string][]time.Time   // per-IP fixed window
	accounts     map[string]*limiterState // per-account password lockout (IP-independent)
	secondFactor map[string]*limiterState // per-account 2FA lockout (password-independent)
	sem          chan struct{}
	now          func() time.Time
}

// NewLoginLimiter returns a ready limiter.
func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{
		attempts:     map[string][]time.Time{},
		accounts:     map[string]*limiterState{},
		secondFactor: map[string]*limiterState{},
		sem:          make(chan struct{}, bcryptConcurrent),
		now:          time.Now,
	}
}

// accountKey normalizes an account identifier (email) for lockout bookkeeping:
// SHA-256 over the lowercased/trimmed email, so map keys are fixed-size and no
// raw address is retained in memory longer than the request.
func accountKey(email string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
	return hex.EncodeToString(sum[:])
}

// pruneStates deletes every fully-expired entry (lock elapsed AND failure
// streak decayed). Called on the write paths so idle keys never accumulate.
func pruneStates(m map[string]*limiterState, now time.Time, decay time.Duration) {
	for k, s := range m {
		if !now.Before(s.expiresAt(decay)) {
			delete(m, k)
		}
	}
}

// evictOldestExpiring removes the entry whose relevance ends soonest, making
// room at the hard cap.
func evictOldestExpiring(m map[string]*limiterState, decay time.Duration) {
	var oldestKey string
	var oldest time.Time
	for k, s := range m {
		if e := s.expiresAt(decay); oldestKey == "" || e.Before(oldest) {
			oldestKey, oldest = k, e
		}
	}
	if oldestKey != "" {
		delete(m, oldestKey)
	}
}

// recordFailureLocked applies one failure to key in m with the given decay/
// threshold/lock policy. escalate=true doubles the lock per extra failure
// (capped at lockMax); false applies the flat lockMax window. Caller holds mu.
func (l *LoginLimiter) recordFailureLocked(m map[string]*limiterState, key string, decay time.Duration, threshold int, lockBase, lockMax time.Duration, escalate bool) {
	now := l.now()
	pruneStates(m, now, decay)
	s := m[key]
	if s == nil {
		if len(m) >= maxLimiterEntries {
			evictOldestExpiring(m, decay)
		}
		s = &limiterState{}
		m[key] = s
	}
	if now.Sub(s.lastFailure) > decay {
		s.failures = 0 // the streak decayed — never a permanent count
	}
	s.failures++
	s.lastFailure = now
	if s.failures >= threshold {
		backoff := lockMax
		if escalate {
			backoff = lockBase << (s.failures - threshold)
			if backoff <= 0 || backoff > lockMax {
				backoff = lockMax
			}
		}
		s.lockedUntil = now.Add(backoff)
	}
}

// allowLocked reports whether key is not currently locked in m. Caller holds mu.
func (l *LoginLimiter) allowLocked(m map[string]*limiterState, key string) bool {
	s := m[key]
	return s == nil || !l.now().Before(s.lockedUntil)
}

// AllowAccount reports whether the account is NOT currently locked out. Check it
// before spending a credential verification so a locked account short-circuits.
func (l *LoginLimiter) AllowAccount(email string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.allowLocked(l.accounts, accountKey(email))
}

// RecordFailure counts a failed login for the account and, past the threshold,
// applies exponential backoff capped at accountLockMax. Keyed by account, so
// rotating source IPs does not evade it; the streak decays, so it is never
// permanent.
func (l *LoginLimiter) RecordFailure(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recordFailureLocked(l.accounts, accountKey(email), accountFailDecay,
		accountFailThreshold, accountLockBase, accountLockMax, true)
}

// ResetAccount clears an account's failures/lockout after a successful login.
// It deliberately does NOT touch the second-factor counter (see
// RecordSecondFactorFailure).
func (l *LoginLimiter) ResetAccount(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.accounts, accountKey(email))
}

// AllowSecondFactor reports whether the account's 2FA step is not locked.
// Checked in LoginTOTP before any code/recovery verification.
func (l *LoginLimiter) AllowSecondFactor(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.allowLocked(l.secondFactor, userID)
}

// RecordSecondFactorFailure counts one failed TOTP/recovery attempt for the
// account (keyed by user id — server-resolved, fixed-size, never
// attacker-chosen). A CORRECT PASSWORD DOES NOT RESET THIS COUNTER: only a
// successful second factor (ResetSecondFactor) clears it, and the streak
// decays after secondFactorFailDecay idle. Past the threshold the 2FA step is
// locked for the flat, bounded secondFactorLock window.
func (l *LoginLimiter) RecordSecondFactorFailure(userID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.recordFailureLocked(l.secondFactor, userID, secondFactorFailDecay,
		secondFactorFailThreshold, secondFactorLock, secondFactorLock, false)
}

// ResetSecondFactor clears the 2FA counter after a successful second factor.
func (l *LoginLimiter) ResetSecondFactor(userID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.secondFactor, userID)
}

// Allow records an attempt for ip and reports whether it is within the
// window. Called before the credential check so failures and successes
// count alike (a success resets via Reset).
func (l *LoginLimiter) Allow(ip string) bool {
	now := l.now()
	cutoff := now.Add(-loginWindow)

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, known := l.attempts[ip]; !known && len(l.attempts) >= maxLimiterEntries {
		// Hard cap: evict the IP whose newest attempt is oldest (fully expired
		// first, else the stalest window) so the map cannot grow without bound.
		var oldestKey string
		var oldest time.Time
		for k, ts := range l.attempts {
			var newest time.Time
			if len(ts) > 0 {
				newest = ts[len(ts)-1]
			}
			if oldestKey == "" || newest.Before(oldest) {
				oldestKey, oldest = k, newest
			}
		}
		if oldestKey != "" {
			delete(l.attempts, oldestKey)
		}
	}

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
// proxy (validated with net.ParseIP), and left it as the direct peer otherwise
// — so this is never a spoofed or non-IP forwarded value.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
