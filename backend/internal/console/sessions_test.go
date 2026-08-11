package console

import (
	"testing"
	"time"
)

func TestSessionsSingleUse(t *testing.T) {
	s := NewSessions()
	id := s.Create(Session{Node: "pve01", GuestType: "qemu", VMID: 101, Kind: "vnc", Proxy: &ProxyTicket{Port: "5900"}})
	if len(id) != 32 {
		t.Fatalf("id length = %d, want 32 hex chars (128-bit)", len(id))
	}

	sess, ok := s.Claim(id)
	if !ok || sess.VMID != 101 {
		t.Fatalf("first claim failed: %v %v", sess, ok)
	}
	if _, ok := s.Claim(id); ok {
		t.Fatal("second claim succeeded — session must be single-use")
	}
	if _, ok := s.Claim("unknown"); ok {
		t.Fatal("unknown id claimed")
	}
}

func TestSessionsExpiry(t *testing.T) {
	s := NewSessions()
	id := s.Create(Session{VMID: 1})
	// Force-expire the entry.
	s.mu.Lock()
	s.byID[id].ExpiresAt = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if _, ok := s.Claim(id); ok {
		t.Fatal("expired session claimed")
	}
}
