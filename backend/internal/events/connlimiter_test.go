package events

import "testing"

// TestConnLimiter is the M3 regression: concurrent SSE streams per user are
// capped, the cap is per-user (not shared), and releasing frees a slot.
func TestConnLimiter(t *testing.T) {
	c := newConnLimiter(2)
	if !c.acquire("u1") || !c.acquire("u1") {
		t.Fatal("acquire within the cap failed")
	}
	if c.acquire("u1") {
		t.Fatal("acquire beyond the cap succeeded")
	}
	if !c.acquire("u2") {
		t.Fatal("a different user should have its own budget")
	}
	c.release("u1")
	if !c.acquire("u1") {
		t.Fatal("release did not free a slot")
	}
}
