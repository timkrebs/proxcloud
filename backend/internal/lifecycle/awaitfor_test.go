package lifecycle

import (
	"testing"
	"time"
)

// TestAwaitForExceedsGrace pins the code-review fix: the stop handler's await
// bound must always exceed the guest's PVE-side grace window (GuestShutdown
// delegates graceful→force to PVE over timeout=graceSec), or the wait would give
// up mid-grace and misread a still-shutting-down guest as a failure.
func TestAwaitForExceedsGrace(t *testing.T) {
	s := &AutoShutdown{}
	tests := []struct {
		graceSec int
		want     time.Duration
	}{
		{30, defaultAwaitTimeout},                 // tiny grace floors at the default
		{120, defaultAwaitTimeout},                // default grace still floored (210s < 300s)
		{300, 300*time.Second + 90*time.Second},   // max grace → grace + margin, above the floor
	}
	for _, tc := range tests {
		got := s.awaitFor(tc.graceSec)
		if got != tc.want {
			t.Fatalf("awaitFor(%d) = %s, want %s", tc.graceSec, got, tc.want)
		}
		if got <= time.Duration(tc.graceSec)*time.Second {
			t.Fatalf("awaitFor(%d) = %s does not exceed the grace window", tc.graceSec, got)
		}
	}
}
