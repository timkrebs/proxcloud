package auth

import (
	"fmt"
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
	if !l.AllowAccount(email) {
		t.Fatal("account locked before the threshold")
	}
	l.RecordFailure(email) // crosses the threshold
	if l.AllowAccount(" victim@example.io ") {
		t.Fatal("account not locked after threshold failures (or key not normalized)")
	}
	if !l.AllowAccount("someone-else@example.io") {
		t.Fatal("an unrelated account was locked — lockout must be per-account")
	}

	// Lock expires after the max backoff.
	l.now = func() time.Time { return base.Add(accountLockMax + time.Second) }
	if !l.AllowAccount(email) {
		t.Fatal("lock did not expire after the backoff")
	}

	// A reset (successful login) clears an active lock.
	l.now = func() time.Time { return base }
	for i := 0; i < accountFailThreshold; i++ {
		l.RecordFailure(email)
	}
	if l.AllowAccount(email) {
		t.Fatal("account not locked")
	}
	l.ResetAccount(email)
	if !l.AllowAccount(email) {
		t.Fatal("ResetAccount did not clear the lock")
	}
}

// TestAccountLockoutDecays: the per-account failure streak is never permanent —
// once accountFailDecay passes since the last failure, the count resets, so an
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
	now = base.Add(accountFailDecay + time.Second)
	// …so the next failure counts as 1, not accountFailThreshold: no lock.
	l.RecordFailure(email)
	if !l.AllowAccount(email) {
		t.Fatal("account locked although the failure streak had decayed")
	}
	// And the lock duration is always bounded: even a long streak caps at
	// accountLockMax, never longer, never permanent.
	for i := 0; i < 50; i++ {
		l.RecordFailure(email)
	}
	if l.AllowAccount(email) {
		t.Fatal("account not locked after a heavy streak")
	}
	now = now.Add(accountLockMax + time.Second)
	if !l.AllowAccount(email) {
		t.Fatal("lock outlasted accountLockMax — must be bounded")
	}
}

// TestAccountMapBounded: the account map is keyed on fixed-size SHA-256 digests
// and hard-capped — 20k distinct emails cannot grow it past maxLimiterEntries.
func TestAccountMapBounded(t *testing.T) {
	l := NewLoginLimiter()
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }
	for i := 0; i < 20_000; i++ {
		l.RecordFailure(fmt.Sprintf("bot-%d@example.io", i))
	}
	l.mu.Lock()
	n := len(l.accounts)
	l.mu.Unlock()
	if n > maxLimiterEntries {
		t.Fatalf("accounts map = %d entries under a distinct-email flood, cap is %d", n, maxLimiterEntries)
	}
	// Keys are digests, not raw emails.
	l.mu.Lock()
	for k := range l.accounts {
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
		if !l.AllowSecondFactor(userID) {
			t.Fatalf("2FA locked early at failure %d", i)
		}
		l.RecordSecondFactorFailure(userID)
		// What Login does on every correct password — must not touch the counter.
		l.ResetAccount("victim@example.io")
		l.Reset("203.0.113.9")
	}
	if l.AllowSecondFactor(userID) {
		t.Fatal("2FA step not locked after threshold failures — a correct password must not reset the counter")
	}
	if !l.AllowSecondFactor("other-user") {
		t.Fatal("an unrelated account's 2FA was locked — counter must be per-account")
	}

	// Bounded window: the lock expires after secondFactorLock.
	now = base.Add(secondFactorLock + time.Second)
	if !l.AllowSecondFactor(userID) {
		t.Fatal("2FA lock outlasted its bounded window")
	}

	// Success clears it outright.
	now = base
	for i := 0; i < secondFactorFailThreshold; i++ {
		l.RecordSecondFactorFailure(userID)
	}
	if l.AllowSecondFactor(userID) {
		t.Fatal("2FA not locked")
	}
	l.ResetSecondFactor(userID)
	if !l.AllowSecondFactor(userID) {
		t.Fatal("ResetSecondFactor did not clear the lock")
	}

	// And the streak decays: failures spread further apart than the decay
	// window never accumulate to a lock.
	now = base.Add(time.Hour)
	for i := 0; i < secondFactorFailThreshold; i++ {
		l.RecordSecondFactorFailure(userID)
		now = now.Add(secondFactorFailDecay + time.Second)
	}
	if !l.AllowSecondFactor(userID) {
		t.Fatal("decayed 2FA failures still accumulated to a lock")
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
