package proxmox

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestSemaphoreRoundTripperCapsConcurrency is the H3 regression: the transport
// wrapping the Proxmox client never lets more than pveMaxConcurrent (here a
// small cap) outbound requests run at once, so a burst queues instead of
// saturating the PVE API.
func TestSemaphoreRoundTripperCapsConcurrency(t *testing.T) {
	const cap = 3
	var inflight, maxSeen int32
	base := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		n := atomic.AddInt32(&inflight, 1)
		for {
			m := atomic.LoadInt32(&maxSeen)
			if n <= m || atomic.CompareAndSwapInt32(&maxSeen, m, n) {
				break
			}
		}
		time.Sleep(15 * time.Millisecond)
		atomic.AddInt32(&inflight, -1)
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	})
	rt := &semaphoreRoundTripper{base: base, sem: make(chan struct{}, cap)}

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest(http.MethodGet, "http://pve/api2/json/version", nil)
			if resp, err := rt.RoundTrip(req); err == nil {
				resp.Body.Close()
			}
		}()
	}
	wg.Wait()
	if maxSeen > cap {
		t.Fatalf("max concurrent PVE requests = %d, want <= %d", maxSeen, cap)
	}
}

// A cancelled request context is honored while waiting for a slot (never hangs).
func TestSemaphoreRoundTripperRespectsContext(t *testing.T) {
	rt := &semaphoreRoundTripper{
		base: roundTripFunc(func(_ *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
		}),
		sem: make(chan struct{}, 1),
	}
	rt.sem <- struct{}{} // occupy the only slot

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://pve", nil)
	if _, err := rt.RoundTrip(req); err == nil {
		t.Fatal("RoundTrip did not honor a cancelled context while waiting for a slot")
	}
}
