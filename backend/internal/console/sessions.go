package console

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// Session TTL: PVE VNC tickets are single-purpose and short-lived; the
// browser connects immediately after the POST, so 25s is generous.
const sessionTTL = 25 * time.Second

// Session is a one-shot handle the browser uses to open the proxied
// websocket. Claimed exactly once, then deleted.
type Session struct {
	ID        string
	Node      string
	GuestType string // qemu | lxc
	VMID      int
	Kind      string // vnc | term
	Proxy     *ProxyTicket
	AuthUser  string // PVE user for the term handshake
	ExpiresAt time.Time
}

// Sessions is the in-memory one-shot session registry.
type Sessions struct {
	mu   sync.Mutex
	byID map[string]*Session
}

// NewSessions starts the registry with a background sweeper owned by the
// caller's context via Close-less design: entries expire on claim.
func NewSessions() *Sessions {
	return &Sessions{byID: map[string]*Session{}}
}

// Create registers a session and returns its unguessable id.
func (s *Sessions) Create(sess Session) string {
	buf := make([]byte, 16)
	_, _ = rand.Read(buf)
	sess.ID = hex.EncodeToString(buf)
	sess.ExpiresAt = time.Now().Add(sessionTTL)

	s.mu.Lock()
	defer s.mu.Unlock()
	// Opportunistic sweep keeps the map tiny without a goroutine.
	now := time.Now()
	for id, old := range s.byID {
		if now.After(old.ExpiresAt) {
			delete(s.byID, id)
		}
	}
	s.byID[sess.ID] = &sess
	return sess.ID
}

// Claim retrieves and removes a session; ok=false for unknown, expired,
// or already-claimed ids.
func (s *Sessions) Claim(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.byID[id]
	if !ok {
		return nil, false
	}
	delete(s.byID, id)
	if time.Now().After(sess.ExpiresAt) {
		return nil, false
	}
	return sess, true
}
