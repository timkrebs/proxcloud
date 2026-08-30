package proxmox

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestSafeSnippetName is the path-confinement guard for the SSH writer: only a
// proxcloud-<name>.yaml filename is accepted; anything with a separator, dot
// segment, or off the allowlist is rejected before it can reach the node.
func TestSafeSnippetName(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"valid", "proxcloud-101-postgresql.yaml", false},
		{"valid digits+hyphens", "proxcloud-9000-my-svc.yaml", false},
		{"empty", "", true},
		{"traversal dotdot", "proxcloud-../etc/passwd.yaml", true},
		{"absolute path", "/var/lib/vz/snippets/proxcloud-1.yaml", true},
		{"forward slash", "sub/proxcloud-1.yaml", true},
		{"backslash", "proxcloud-1\\evil.yaml", true},
		{"wrong prefix", "evil-1.yaml", true},
		{"wrong extension", "proxcloud-1.txt", true},
		{"uppercase", "proxcloud-ABC.yaml", true},
		{"dotdot only", "..", true},
		{"underscore not allowed", "proxcloud-a_b.yaml", true},
		{"double extension traversal", "proxcloud-1.yaml/../x", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := SafeSnippetName(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("SafeSnippetName(%q) err = %v, wantErr = %v", tt.in, err, tt.wantErr)
			}
		})
	}
}

// countingCloser records how many times it was closed, so a test can assert the
// watchdog did (or did not) tear down the connection.
type countingCloser struct {
	mu     sync.Mutex
	closed int
}

func (c *countingCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed++
	return nil
}

func (c *countingCloser) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// TestCloseOnDoneClosesOnCancel proves the SFTP write watchdog: once the dial
// deadline is cleared, a canceled context (op timeout or parent cancel) must close
// the underlying conn so a stalled post-handshake Write/Close/rename returns
// promptly instead of hanging past the op timeout.
func TestCloseOnDoneClosesOnCancel(t *testing.T) {
	c := &countingCloser{}
	ctx, cancel := context.WithCancel(context.Background())
	stop := closeOnDone(ctx, c)
	defer stop()

	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if c.count() == 1 {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("watchdog did not close the conn after context cancel (closed=%d)", c.count())
}

// TestCloseOnDoneStopDoesNotClose proves the normal-completion path: calling stop
// (as closeFn does) unregisters the watchdog so a FINISHED op never has its conn
// closed out from under the next caller.
func TestCloseOnDoneStopDoesNotClose(t *testing.T) {
	c := &countingCloser{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := closeOnDone(ctx, c)
	stop()
	// Cancelling AFTER stop must not close: the watchdog already unregistered.
	cancel()

	time.Sleep(20 * time.Millisecond)
	if n := c.count(); n != 0 {
		t.Fatalf("watchdog closed the conn after stop (closed=%d), want 0", n)
	}
}

// TestNewSnippetWriterRequiresConfig proves the writer fails fast on incomplete
// config — there is no insecure fallback (host-key verification is mandatory).
func TestNewSnippetWriterRequiresConfig(t *testing.T) {
	_, err := NewSnippetWriter(SnippetConfig{}) // all empty
	if err == nil {
		t.Fatal("NewSnippetWriter accepted an empty config")
	}
	// Missing known_hosts is specifically required (no InsecureIgnoreHostKey path).
	_, err = NewSnippetWriter(SnippetConfig{
		Host: "node", User: "snip", KeyPath: "/nonexistent", StoragePath: "/var/lib/vz/snippets",
	})
	if err == nil {
		t.Fatal("NewSnippetWriter accepted a config with no known_hosts")
	}
}
