package auth

import (
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
