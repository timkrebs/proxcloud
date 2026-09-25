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

	// Second-factor (TOTP/recovery-code) counter, keyed per account (user id)
	// and NOT reset by a correct password: ~10 consecutive failures lock the
	// 2FA step (not the account) for a bounded window. Without this, a
	// known-password attacker re-runs Login (which legitimately clears the
	// password lockout) to mint fresh challenges and brute-forces TOTP at
	// Argon2 speed.
	secondFactorFailThreshold = 10
	secondFactorLock          = 15 * time.Minute

	// failureDecay: a failure streak resets only after this long WITHOUT a
	// failure. It must be far longer than the maximum lock: if the streak
	// decayed as soon as a lock expired, an attacker would earn a fresh set of
	// threshold guesses after every lock instead of one guess per lock — the
	// streak must still be at the threshold when the lock lifts, so the very
	// next failure re-locks.
	failureDecay = 2 * time.Hour

	// maxEmailBytes: RFC 5321's address ceiling. Longer inputs are rejected by
	// the handler BEFORE any limiter map is touched, so the account maps never
	// hash attacker-sized inputs (keys are fixed-size SHA-256 anyway).
	maxEmailBytes = 254

	// maxLimiterEntries hard-caps each limiter map so a distinct-key flood
	// cannot grow memory without bound.
	maxLimiterEntries = 10_000
	// maxLockedEntries caps a failure tracker's keys AT the threshold (locked,
	// or re-locked by their next failure), which are never evicted. Each one
	// costs an attacker threshold failed password hashes, and the hash
	// semaphore bounds those globally: at the ~70 verifications/s the third
	// security review measured (Argon2id, bcryptConcurrent slots, Apple M4
	// Pro), at most ~100k keys can reach the threshold within one
	// failureDecay. The cap sits well above that (~50 MB were it ever full);
	// past it the tracker fails closed rather than stop counting.
	maxLockedEntries = 1 << 18
	// limiterPruneEvery is the minimum gap between sweeps of expired entries,
	// so a key flood cannot force a full-map scan on every request.
	limiterPruneEvery = 10 * time.Second
)

// limiterState tracks a decaying failure streak for one key and, once the
// threshold is crossed, the time the lock expires.
type limiterState struct {
	failures    int
	lastFailure time.Time
	lockedUntil time.Time
	// blockSeen: a request has already been refused during the current lock —
	// lets the caller audit the first refusal of each lock, not every one.
	blockSeen bool
}

// expiresAt is the instant this entry stops mattering: the later of lock
// expiry and failure decay.
func (s *limiterState) expiresAt() time.Time {
	e := s.lastFailure.Add(failureDecay)
	if s.lockedUntil.After(e) {
		return s.lockedUntil
	}
	return e
}

// failureTracker is one bounded, decaying failure map and its lock policy.
// Callers hold LoginLimiter.mu.
//
// Keys below the threshold live in streaks, capped at maxStreaks: when it is
// full, the unprotected entry whose relevance ends soonest makes room, so junk
// keys can only push out a streak of fewer than threshold failures, and only
// after cycling out every older streak first. A key reaching the threshold
// moves to locks, which nothing is ever evicted from and which is capped far
// above what the hash rate can fill (maxLockedEntries). Were locks full, the
// key would stay in streaks, protected there; once streaks too held nothing
// evictable, a new key could not be counted and callers refuse it (canTrack):
// fail closed, never uncounted guesses.
type failureTracker struct {
	streaks    map[string]*limiterState
	locks      map[string]*limiterState
	maxStreaks int
	maxLocks   int
	lastPrune  time.Time
	threshold  int
	lockBase   time.Duration
	lockMax    time.Duration
	escalate   bool // double the lock per failure past the threshold (capped)
}

func newFailureTracker(threshold int, lockBase, lockMax time.Duration, escalate bool) *failureTracker {
	return &failureTracker{
		streaks:    map[string]*limiterState{},
		locks:      map[string]*limiterState{},
		maxStreaks: maxLimiterEntries,
		maxLocks:   maxLockedEntries,
		threshold:  threshold, lockBase: lockBase, lockMax: lockMax, escalate: escalate,
	}
}

// protected reports whether an entry must never be evicted to make room: it
// is locked now, or its streak has reached the threshold (its next failure
// re-locks). Evicting such an entry would unlock an account under attack — a
// flood of junk keys must never buy an attacker a victim's lock.
func (t *failureTracker) protected(s *limiterState, now time.Time) bool {
	if !now.Before(s.expiresAt()) {
		return false // lock over and streak decayed: garbage awaiting the next sweep
	}
	return now.Before(s.lockedUntil) || s.failures >= t.threshold
}

// get returns key's live entry, or nil — dropping it if it has expired.
func (t *failureTracker) get(key string, now time.Time) *limiterState {
	for _, m := range [...]map[string]*limiterState{t.locks, t.streaks} {
		if s := m[key]; s != nil {
			if now.Before(s.expiresAt()) {
				return s
			}
			delete(m, key)
			return nil
		}
	}
	return nil
}

// locked reports whether key is locked at now.
func (t *failureTracker) locked(key string, now time.Time) bool {
	s := t.get(key, now)
	return s != nil && now.Before(s.lockedUntil)
}

// canTrack reports whether a failure for key would be counted: key is
// tracked already, or streaks has, or can make, room for it.
func (t *failureTracker) canTrack(key string, now time.Time) bool {
	return t.get(key, now) != nil || t.makeRoom(now)
}

// reset forgets key's failures and any lock.
func (t *failureTracker) reset(key string) {
	delete(t.streaks, key)
	delete(t.locks, key)
}

// recordFailure applies one failure to key. engaged reports that this failure
// set a lock. tracked is false only when key is new and the tracker is
// saturated (see failureTracker): the failure then goes uncounted rather than
// evicting an active lock, which is why callers refuse such keys up front.
func (t *failureTracker) recordFailure(key string, now time.Time) (engaged, tracked bool) {
	t.pruneExpired(now)
	s := t.get(key, now)
	if s == nil {
		if !t.makeRoom(now) {
			return false, false
		}
		s = &limiterState{}
		t.streaks[key] = s
	}
	if now.Sub(s.lastFailure) > failureDecay {
		s.failures = 0 // the streak decayed — never a permanent count
	}
	s.failures++
	s.lastFailure = now
	if s.failures >= t.threshold {
		backoff := t.lockMax
		if t.escalate {
			backoff = t.lockBase << (s.failures - t.threshold)
			if backoff <= 0 || backoff > t.lockMax {
				backoff = t.lockMax
			}
		}
		s.lockedUntil = now.Add(backoff)
		s.blockSeen = false
		engaged = true
		if _, inStreaks := t.streaks[key]; inStreaks && len(t.locks) < t.maxLocks {
			delete(t.streaks, key)
			t.locks[key] = s
		}
	}
	return engaged, true
}

// pruneExpired sweeps expired entries from both maps, at most once per
// limiterPruneEvery so a key flood cannot force a scan per request.
func (t *failureTracker) pruneExpired(now time.Time) {
	if now.Sub(t.lastPrune) < limiterPruneEvery {
		return
	}
	t.lastPrune = now
	for _, m := range [...]map[string]*limiterState{t.streaks, t.locks} {
		for k, s := range m {
			if !now.Before(s.expiresAt()) {
				delete(m, k)
			}
		}
	}
}

// makeRoom reports whether streaks can take one more key, evicting the
// UNPROTECTED entry whose relevance ends soonest when it is full. O(n) over at
// most maxStreaks, but it only runs when a new key meets a full map, on
// failed-credential paths already bounded by the per-IP limiter and the
// password-hash semaphore.
func (t *failureTracker) makeRoom(now time.Time) bool {
	if len(t.streaks) < t.maxStreaks {
		return true
	}
	var victim string
	var soonest time.Time
	for k, s := range t.streaks {
		if t.protected(s, now) {
			continue
		}
		if e := s.expiresAt(); victim == "" || e.Before(soonest) {
			victim, soonest = k, e
		}
	}
	if victim == "" {
		return false
	}
	delete(t.streaks, victim)
	return true
}

// LoginLimiter is safe for concurrent use.
//
// The password lockout keys on the email alone, whether or not an account
// exists: every email locks, escalates and survives floods alike, so a lockout
// reveals nothing about which emails are registered.
type LoginLimiter struct {
	mu           sync.Mutex
	attempts     map[string][]time.Time // per-IP fixed window, keyed by LimiterIPKey
	lastIPPrune  time.Time
	accounts     *failureTracker // per-account password lockout (IP-independent)
	secondFactor *failureTracker // per-account 2FA lockout (password-independent)
	sem          chan struct{}
	now          func() time.Time
}

// NewLoginLimiter returns a ready limiter.
func NewLoginLimiter() *LoginLimiter {
	return &LoginLimiter{
		attempts:     map[string][]time.Time{},
		accounts:     newFailureTracker(accountFailThreshold, accountLockBase, accountLockMax, true),
		secondFactor: newFailureTracker(secondFactorFailThreshold, secondFactorLock, secondFactorLock, false),
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

// AllowAccount reports whether a login for email may proceed to credential
// verification; check it before spending a password hash so a locked account
// short-circuits. It refuses a locked account and, failing closed, one whose
// failures the tracker could not count; saturated reports that second case,
// which takes a flood of locked keys far beyond the hash rate's reach.
func (l *LoginLimiter) AllowAccount(email string) (allowed, saturated bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	key, now := accountKey(email), l.now()
	if l.accounts.locked(key, now) {
		return false, false
	}
	if !l.accounts.canTrack(key, now) {
		return false, true
	}
	return true, false
}

// RecordFailure counts a failed login for email and, past the threshold,
// applies exponential backoff capped at accountLockMax. Keyed by account, so
// rotating source IPs does not evade it; the streak decays, so it is never
// permanent.
func (l *LoginLimiter) RecordFailure(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.accounts.recordFailure(accountKey(email), l.now())
}

// ResetAccount clears an account's failures/lockout after a successful login.
// It deliberately does NOT touch the second-factor counter (see
// ReserveSecondFactorAttempt).
func (l *LoginLimiter) ResetAccount(email string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.accounts.reset(accountKey(email))
}

// SecondFactorVerdict is the outcome of reserving one second-factor attempt.
type SecondFactorVerdict struct {
	// Allowed: the attempt may be verified. When false, refuse it (429).
	Allowed bool
	// EngagesLock: this attempt brought the streak to the threshold — if it
	// fails, the 2FA step is now locked. Worth recording in the audit trail.
	EngagesLock bool
	// FirstBlock: Allowed is false and this is the first refused attempt of
	// the current lock — audit it once rather than on every refusal.
	FirstBlock bool
}

// ReserveSecondFactorAttempt checks the account's 2FA lock and, when it is
// open, counts this attempt as a failure in the SAME critical section. Counting
// up front is what makes the bound hold under concurrency: requests racing the
// last few slots each take one, and the one that reaches the threshold engages
// the lock for everything after it — none can slip past a check another is
// about to invalidate. A successful second factor refunds the attempts
// (ResetSecondFactor). A correct PASSWORD never resets this counter. Keyed by
// user id — server-resolved, never attacker-chosen. When the attempt cannot be
// counted at all (map full of locked accounts), it is refused: fail closed.
func (l *LoginLimiter) ReserveSecondFactorAttempt(userID string) SecondFactorVerdict {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if s := l.secondFactor.get(userID, now); s != nil && now.Before(s.lockedUntil) {
		first := !s.blockSeen
		s.blockSeen = true
		return SecondFactorVerdict{FirstBlock: first}
	}
	engaged, tracked := l.secondFactor.recordFailure(userID, now)
	if !tracked {
		return SecondFactorVerdict{}
	}
	return SecondFactorVerdict{Allowed: true, EngagesLock: engaged}
}

// secondFactorLocked reports whether the account's 2FA step is locked.
func (l *LoginLimiter) secondFactorLocked(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.secondFactor.locked(userID, l.now())
}

// ResetSecondFactor clears the 2FA counter after a successful second factor.
func (l *LoginLimiter) ResetSecondFactor(userID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.secondFactor.reset(userID)
}

// Allow records an attempt from ip and reports whether it is within the
// window. Called before the credential check so failures and successes count
// alike (a success resets via Reset). Keyed by LimiterIPKey, so an IPv6 client
// cannot rotate through its /64 for fresh windows.
func (l *LoginLimiter) Allow(ip string) bool {
	key := LimiterIPKey(ip)
	now := l.now()
	cutoff := now.Add(-loginWindow)

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, known := l.attempts[key]; !known {
		l.makeRoomForIPLocked(now, cutoff)
	}
	kept := l.attempts[key][:0]
	for _, t := range l.attempts[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= loginMaxPerIP {
		l.attempts[key] = kept
		return false
	}
	l.attempts[key] = append(kept, now)
	return true
}

// makeRoomForIPLocked keeps the per-IP map under maxLimiterEntries without a
// scan per request: expired windows are swept at most once per
// limiterPruneEvery, and if the map is still full an arbitrary entry is
// dropped (O(1)). Dropping an IP window only forgets that IP's recent
// attempts; it grants no one more than a fresh window.
func (l *LoginLimiter) makeRoomForIPLocked(now, cutoff time.Time) {
	if len(l.attempts) < maxLimiterEntries {
		return
	}
	if now.Sub(l.lastIPPrune) >= limiterPruneEvery {
		l.lastIPPrune = now
		for k, ts := range l.attempts {
			if len(ts) == 0 || !ts[len(ts)-1].After(cutoff) {
				delete(l.attempts, k)
			}
		}
	}
	for k := range l.attempts {
		if len(l.attempts) < maxLimiterEntries {
			break
		}
		delete(l.attempts, k)
	}
}

// Reset clears an IP's window after a successful login.
func (l *LoginLimiter) Reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.attempts, LimiterIPKey(ip))
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
// — so this is never a spoofed or non-IP forwarded value. It is the full
// address, as recorded in audit and session rows; rate limiters key on
// LimiterIPKey instead.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// LimiterIPKey is the rate-limit key for a client address. An IPv6 client is
// bucketed by its /64: a single subscriber typically controls a whole /64 (2^64
// addresses), so per-address keys would hand one attacker unlimited fresh
// buckets. IPv4 (including IPv4-mapped IPv6) keys on the full address. A value
// that is not an IP is returned unchanged.
func LimiterIPKey(ip string) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String()
	}
	return parsed.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
