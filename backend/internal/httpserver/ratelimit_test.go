package httpserver

import (
	"testing"
	"time"
)

// TestAPIRateLimiter is the H3 regression: a client is allowed up to `limit`
// requests within the window and blocked beyond it, and the window slides so a
// later request is allowed again.
func TestAPIRateLimiter(t *testing.T) {
	l := newAPIRateLimiter(3, time.Minute)
	base := time.Unix(1_700_000_000, 0)
	l.now = func() time.Time { return base }

	for i := 0; i < 3; i++ {
		if !l.allow("1.2.3.4") {
			t.Fatalf("request %d blocked within budget", i)
		}
	}
	if l.allow("1.2.3.4") {
		t.Fatal("4th request within the window was not rate-limited")
	}
	// A different client has its own budget.
	if !l.allow("5.6.7.8") {
		t.Fatal("unrelated client wrongly throttled")
	}
	// After the window slides, the first client is allowed again.
	l.now = func() time.Time { return base.Add(time.Minute + time.Second) }
	if !l.allow("1.2.3.4") {
		t.Fatal("request not allowed after the window elapsed")
	}
}
