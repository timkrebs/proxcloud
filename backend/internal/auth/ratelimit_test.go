package auth

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestAccountLockout is the H2 regression: an account is locked out after enough
// consecutive failures INDEPENDENT of source IP (so a distributed attack cannot
// grind it), the lock expires after the backoff, and a success resets it.
func TestAccountLockout(t *testing.T) {
	l := NewLoginLimiter()
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }
	const email = "Victim@Example.IO" // case/space-insensitive keying

	for i := 0; i < accountFailThreshold-1; i++ {
		l.RecordFailure(email)
	}
	if !allowed(l, email) {
		t.Fatal("account locked before the threshold")
	}
	l.RecordFailure(email) // crosses the threshold
	if allowed(l, " victim@example.io ") {
		t.Fatal("account not locked after threshold failures (or key not normalized)")
	}
	if !allowed(l, "someone-else@example.io") {
		t.Fatal("an unrelated account was locked — lockout must be per-account")
	}

	// Lock expires after the max backoff.
	l.now = func() time.Time { return base.Add(accountLockMax + time.Second) }
	if !allowed(l, email) {
		t.Fatal("lock did not expire after the backoff")
	}

	// A reset (successful login) clears an active lock.
	l.now = func() time.Time { return base }
	for i := 0; i < accountFailThreshold; i++ {
		l.RecordFailure(email)
	}
	if allowed(l, email) {
		t.Fatal("account not locked")
	}
	l.ResetAccount(email)
	if !allowed(l, email) {
		t.Fatal("ResetAccount did not clear the lock")
	}
}

// TestAccountLockoutDecays: the per-account failure streak is never permanent —
// once failureDecay passes since the last failure, the count resets, so an
// account that saw sporadic failures long ago starts from a clean slate.
func TestAccountLockoutDecays(t *testing.T) {
	l := NewLoginLimiter()
	base := time.Unix(1_700_000_000, 0)
	now := base
	l.now = func() time.Time { return now }
	const email = "victim@example.io"

	// One failure short of the threshold…
	for i := 0; i < accountFailThreshold-1; i++ {
		l.RecordFailure(email)
	}
	// …then the streak decays…
	now = base.Add(failureDecay + time.Second)
	// …so the next failure counts as 1, not accountFailThreshold: no lock.
	l.RecordFailure(email)
	if !allowed(l, email) {
		t.Fatal("account locked although the failure streak had decayed")
	}
	// And the lock duration is always bounded: even a long streak caps at
	// accountLockMax, never longer, never permanent.
	for i := 0; i < 50; i++ {
		l.RecordFailure(email)
	}
	if allowed(l, email) {
		t.Fatal("account not locked after a heavy streak")
	}
	now = now.Add(accountLockMax + time.Second)
	if !allowed(l, email) {
		t.Fatal("lock outlasted accountLockMax — must be bounded")
	}
}

// allowed is AllowAccount's verdict alone.
func allowed(l *LoginLimiter, email string) bool {
	ok, _ := l.AllowAccount(email)
	return ok
}

// TestAccountMapBounded: the account maps are keyed on fixed-size SHA-256
// digests and hard-capped — distinct emails past the cap cannot grow the
// streak map beyond maxLimiterEntries.
func TestAccountMapBounded(t *testing.T) {
	l := NewLoginLimiter()
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }
	for i := 0; i < maxLimiterEntries+2_000; i++ {
		l.RecordFailure(fmt.Sprintf("bot-%d@example.io", i))
	}
	l.mu.Lock()
	n := len(l.accounts.streaks)
	l.mu.Unlock()
	if n > maxLimiterEntries {
		t.Fatalf("streak map = %d entries under a distinct-email flood, cap is %d", n, maxLimiterEntries)
	}
	// Keys are digests, not raw emails.
	l.mu.Lock()
	for k := range l.accounts.streaks {
		if len(k) != 64 { // hex SHA-256
			t.Fatalf("account key %q is not a fixed-size digest", k)
		}
		break
	}
	l.mu.Unlock()
}

// TestSecondFactorCounterSurvivesPasswordSuccess is the H2-followup regression:
// the 2FA failure counter is keyed per account and is NOT cleared by the
// password-success resets (ResetAccount / per-IP Reset) — only a successful
// second factor clears it — so a known-password attacker cannot mint fresh
// challenges to grind TOTP forever. It locks at the threshold for a bounded
// window and decays when idle.
func TestSecondFactorCounterSurvivesPasswordSuccess(t *testing.T) {
	l := NewLoginLimiter()
	base := time.Unix(1_700_000_000, 0)
	now := base
	l.now = func() time.Time { return now }
	const userID = "user-uuid-1"

	for i := 0; i < secondFactorFailThreshold; i++ {
		if v := l.ReserveSecondFactorAttempt(userID); !v.Allowed {
			t.Fatalf("2FA locked early at attempt %d", i)
		}
		// What Login does on every correct password — must not touch the counter.
		l.ResetAccount("victim@example.io")
		l.Reset("203.0.113.9")
	}
	if !l.secondFactorLocked(userID) {
		t.Fatal("2FA step not locked after threshold failures — a correct password must not reset the counter")
	}
	if l.secondFactorLocked("other-user") {
		t.Fatal("an unrelated account's 2FA was locked — counter must be per-account")
	}

	// Bounded window: the lock expires after secondFactorLock.
	now = base.Add(secondFactorLock + time.Second)
	if l.secondFactorLocked(userID) {
		t.Fatal("2FA lock outlasted its bounded window")
	}

	// Success clears it outright.
	now = base
	l.ResetSecondFactor(userID)
	for i := 0; i < secondFactorFailThreshold; i++ {
		l.ReserveSecondFactorAttempt(userID)
	}
	if !l.secondFactorLocked(userID) {
		t.Fatal("2FA not locked")
	}
	l.ResetSecondFactor(userID)
	if l.secondFactorLocked(userID) {
		t.Fatal("ResetSecondFactor did not clear the lock")
	}

	// And the streak decays: attempts spread further apart than the decay
	// window never accumulate to a lock.
	now = base.Add(24 * time.Hour)
	for i := 0; i < secondFactorFailThreshold; i++ {
		l.ReserveSecondFactorAttempt(userID)
		now = now.Add(failureDecay + time.Second)
	}
	if l.secondFactorLocked(userID) {
		t.Fatal("decayed 2FA failures still accumulated to a lock")
	}
}

// TestLockEvictionNeverUnlocks replays the second review's proof of concept: a
// victim locked at the maximum, then a flood of failures on other emails, past
// the streak cap so it forces evictions. The old hard-cap eviction preferred
// the victim's lock (it expired soonest), so the flood unlocked the account and
// erased its streak. A locked or at-threshold entry must never be evicted —
// whether it sits in the lock map or, with that map full, stayed behind in the
// streak map.
func TestLockEvictionNeverUnlocks(t *testing.T) {
	for _, tt := range []struct {
		name     string
		maxLocks int
	}{
		{"lock map has room", maxLockedEntries},
		{"lock map full", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l := NewLoginLimiter()
			l.accounts.maxStreaks, l.accounts.maxLocks = 64, tt.maxLocks
			base := time.Unix(1_700_000_000, 0)
			l.now = func() time.Time { return base }
			const victim = "victim@example.io"

			for i := 0; i < accountFailThreshold+4; i++ { // escalated to the max lock
				l.RecordFailure(victim)
			}
			for i := 0; i < 4*64; i++ {
				l.RecordFailure(fmt.Sprintf("account-%d@example.io", i))
			}
			if allowed(l, victim) {
				t.Fatal("a failure flood evicted the victim's active lock")
			}
			l.mu.Lock()
			s := l.accounts.get(accountKey(victim), base)
			l.mu.Unlock()
			if s == nil || s.failures != accountFailThreshold+4 {
				t.Fatalf("victim's failure streak was not preserved: %+v", s)
			}
		})
	}
}

// TestJunkFloodCannotUntrackAccounts replays the third review's proof of
// concept with the production caps: drive 12k junk emails to lockout, then
// guess a fresh account's password. Before, locks shared the 10k-entry map:
// full of them, it could not take the fresh account, which then went
// untracked — 1,000 wrong guesses left it unlocked. Locked keys now have their
// own map, far larger than the hash rate can fill, so every junk lock stays
// and the fresh account still locks at the threshold.
func TestJunkFloodCannotUntrackAccounts(t *testing.T) {
	l := NewLoginLimiter()
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }
	const junk = maxLimiterEntries + 2_000

	for i := 0; i < junk; i++ {
		for j := 0; j < accountFailThreshold; j++ {
			l.RecordFailure(fmt.Sprintf("junk-%d@example.io", i))
		}
	}
	if n := len(l.accounts.locks); n != junk {
		t.Fatalf("lock map holds %d junk locks, want all %d", n, junk)
	}
	const victim = "fresh-victim@example.io"
	for i := 0; i < accountFailThreshold; i++ {
		if !allowed(l, victim) {
			t.Fatalf("victim refused at attempt %d, before reaching the threshold", i)
		}
		l.RecordFailure(victim)
	}
	if allowed(l, victim) {
		t.Fatal("the fresh account was not locked after the threshold, despite the junk flood")
	}
}

// TestFullAccountTrackerFailsClosed: were both maps ever saturated — every
// lock slot taken and nothing evictable left in the streak map — an email not
// already tracked is refused, and reported as saturation, rather than allowed
// with its failures uncounted; and no lock is evicted to make room. The caps
// are shrunk here; in production they sit far beyond the hash rate's reach.
func TestFullAccountTrackerFailsClosed(t *testing.T) {
	l := NewLoginLimiter()
	l.accounts.maxStreaks, l.accounts.maxLocks = 8, 16
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }
	const locked = 8 + 16

	for i := 0; i < locked; i++ {
		for j := 0; j < accountFailThreshold; j++ {
			l.RecordFailure(fmt.Sprintf("locked-%d@example.io", i))
		}
	}
	if ok, saturated := l.AllowAccount("newcomer@example.io"); ok || !saturated {
		t.Fatalf("untracked email while saturated = (allowed %v, saturated %v), want (false, true)", ok, saturated)
	}
	l.RecordFailure("newcomer@example.io")
	for i := 0; i < locked; i++ {
		if ok, saturated := l.AllowAccount(fmt.Sprintf("locked-%d@example.io", i)); ok || saturated {
			t.Fatalf("locked email %d = (allowed %v, saturated %v), want a plain lock", i, ok, saturated)
		}
	}
	// Once the locks have expired and the streaks decayed, the slots are
	// reclaimable again.
	l.now = func() time.Time { return base.Add(failureDecay + time.Minute) }
	if !allowed(l, "newcomer@example.io") {
		t.Fatal("the tracker did not recover after its entries expired")
	}
}

// TestLockoutRelocksAfterExpiry: the streak outlives the lock, so once an
// account has been driven into lockout, each lock's expiry buys an attacker
// exactly one more guess — the next failure re-locks at once. Before, the
// streak decayed with the lock and every expiry bought a fresh threshold of
// guesses.
func TestLockoutRelocksAfterExpiry(t *testing.T) {
	l := NewLoginLimiter()
	base := time.Unix(1_700_000_000, 0)
	now := base
	l.now = func() time.Time { return now }
	const email = "victim@example.io"

	for i := 0; i < accountFailThreshold+4; i++ {
		l.RecordFailure(email)
	}
	now = now.Add(accountLockMax + time.Second)
	if !allowed(l, email) {
		t.Fatal("the lock did not expire")
	}
	l.RecordFailure(email)
	if allowed(l, email) {
		t.Fatal("one failure after the lock expired did not re-lock the account")
	}
}

// TestSecondFactorReserveIsAtomic: many concurrent attempts racing a fresh
// account's budget — exactly the threshold get through, never more. With the
// old check-then-record, all of them could pass the check first.
func TestSecondFactorReserveIsAtomic(t *testing.T) {
	l := NewLoginLimiter()
	const userID = "user-uuid-race"
	var allowed int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.ReserveSecondFactorAttempt(userID).Allowed {
				atomic.AddInt32(&allowed, 1)
			}
		}()
	}
	wg.Wait()
	if allowed != secondFactorFailThreshold {
		t.Fatalf("%d concurrent attempts were allowed, want exactly %d", allowed, secondFactorFailThreshold)
	}
}

// TestSecondFactorVerdictAuditSignals: the attempt that reaches the threshold
// reports EngagesLock, and only the first refusal of each lock reports
// FirstBlock — so the audit trail records every lock without one row per
// refused request.
func TestSecondFactorVerdictAuditSignals(t *testing.T) {
	l := NewLoginLimiter()
	base := time.Unix(1_700_000_000, 0)
	now := base
	l.now = func() time.Time { return now }
	const userID = "user-uuid-audit"

	for i := 1; i <= secondFactorFailThreshold; i++ {
		v := l.ReserveSecondFactorAttempt(userID)
		if !v.Allowed || v.EngagesLock != (i == secondFactorFailThreshold) {
			t.Fatalf("attempt %d: %+v, want allowed with EngagesLock only on the last", i, v)
		}
	}
	if v := l.ReserveSecondFactorAttempt(userID); v.Allowed || !v.FirstBlock {
		t.Fatalf("first refusal = %+v, want refused with FirstBlock", v)
	}
	if v := l.ReserveSecondFactorAttempt(userID); v.Allowed || v.FirstBlock {
		t.Fatalf("second refusal = %+v, want refused without FirstBlock", v)
	}
	// The lock lifts; the streak has not decayed, so the next attempt re-locks
	// and its first refusal is reported again.
	now = now.Add(secondFactorLock + time.Second)
	if v := l.ReserveSecondFactorAttempt(userID); !v.Allowed || !v.EngagesLock {
		t.Fatalf("attempt after the lock lifted = %+v, want allowed and re-locking", v)
	}
	if v := l.ReserveSecondFactorAttempt(userID); v.Allowed || !v.FirstBlock {
		t.Fatalf("first refusal of the new lock = %+v, want FirstBlock", v)
	}
}

// TestLimiterIPKey: IPv6 clients are bucketed by /64, everything else keys on
// the full address.
func TestLimiterIPKey(t *testing.T) {
	tests := []struct{ in, want string }{
		{"203.0.113.9", "203.0.113.9"},
		{"::ffff:203.0.113.9", "203.0.113.9"},
		{"2001:db8:1:2:aaaa:bbbb:cccc:dddd", "2001:db8:1:2::/64"},
		{"2001:db8:1:2::1", "2001:db8:1:2::/64"},
		{"2001:db8:1:3::1", "2001:db8:1:3::/64"},
		{"not-an-ip", "not-an-ip"},
	}
	for _, tt := range tests {
		if got := LimiterIPKey(tt.in); got != tt.want {
			t.Errorf("LimiterIPKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestLoginLimiterIPv6SharesWindow: rotating addresses inside one /64 does not
// buy fresh login windows; a different /64 is a different client.
func TestLoginLimiterIPv6SharesWindow(t *testing.T) {
	l := NewLoginLimiter()
	for i := 0; i < loginMaxPerIP; i++ {
		if !l.Allow(fmt.Sprintf("2001:db8:1:2::%x", i+1)) {
			t.Fatalf("attempt %d blocked too early", i)
		}
	}
	if l.Allow("2001:db8:1:2::ffff") {
		t.Fatal("a fresh address in the same /64 got a fresh window")
	}
	if !l.Allow("2001:db8:1:3::1") {
		t.Fatal("a different /64 was throttled")
	}
}

func TestLoginLimiter(t *testing.T) {
	l := NewLoginLimiter()
	for i := 0; i < loginMaxPerIP; i++ {
		if !l.Allow("10.0.0.1") {
			t.Fatalf("attempt %d blocked too early", i)
		}
	}
	if l.Allow("10.0.0.1") {
		t.Fatal("attempt beyond the window allowed")
	}
	if !l.Allow("10.0.0.2") {
		t.Fatal("other IP must not be affected")
	}
	l.Reset("10.0.0.1")
	if !l.Allow("10.0.0.1") {
		t.Fatal("reset did not clear the window")
	}
}
