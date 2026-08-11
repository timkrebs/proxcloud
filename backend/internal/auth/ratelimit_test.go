package auth

import "testing"

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
