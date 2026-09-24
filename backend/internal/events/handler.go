package events

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/auth"
)

const (
	heartbeatInterval = 25 * time.Second
	// ownedRefreshInterval is how often the per-connection owned-VMID set is
	// recomputed from the session's active tenant (ADR-0011). Cheap, indexed.
	ownedRefreshInterval = 5 * time.Second
	ownedFetchTimeout    = 5 * time.Second
	// sseWriteTimeout bounds each individual SSE write: the server runs with no
	// global WriteTimeout (a stream must outlive it), so without a per-write
	// deadline a client that stops reading pins the goroutine + buffers forever.
	// Renewed before every write (heartbeats arrive every 25s < 30s, so an
	// armed deadline never fires between healthy writes); a failed write drops
	// the connection.
	sseWriteTimeout = 30 * time.Second
)

// OwnedVMIDsFunc resolves the set of VMIDs a tenant owns (active or pending) for
// SSE per-connection task/deployment-event filtering. Server-derived from the
// session's active tenant, never client-asserted (ADR-0011).
type OwnedVMIDsFunc func(ctx context.Context, tenantID string) (map[int]bool, error)

// Handler serves GET /api/events as a Server-Sent-Events stream. The route
// must be mounted outside the global request-timeout middleware, and inside the
// authenticated group so the subscriber's *auth.Identity is in context.
//
// Tenant scoping (ADR-0011), enforced per connection:
//   - node-metrics frames are delivered ONLY to platform-admin subscribers.
//   - task/deployment frames are delivered only when the event's VMID is owned
//     by the subscriber's active tenant; platform-admin bypasses the filter.
//
// The owned-VMID set is derived server-side from session.active_tenant_id and
// refreshed on ownedRefreshInterval; a client cannot widen its own view.
// maxSSEPerUser caps concurrent SSE streams per user (generous for multiple
// browser tabs; blocks unbounded stream-open abuse).
const maxSSEPerUser = 10

// connLimiter counts concurrent connections per key and bounds them, so no
// single user can hold unbounded SSE streams.
type connLimiter struct {
	mu    sync.Mutex
	count map[string]int
	max   int
}

func newConnLimiter(max int) *connLimiter { return &connLimiter{count: map[string]int{}, max: max} }

func (c *connLimiter) acquire(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count[key] >= c.max {
		return false
	}
	c.count[key]++
	return true
}

func (c *connLimiter) release(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.count[key] > 0 {
		c.count[key]--
		if c.count[key] == 0 {
			delete(c.count, key)
		}
	}
}

func Handler(b *Broker, log *slog.Logger, owned OwnedVMIDsFunc) http.HandlerFunc {
	// Shared across all connections: cap concurrent SSE streams per user so one
	// tenant cannot open unbounded streams (each holds broker state and, for
	// non-admins, re-queries owned VMIDs every few seconds).
	sseConns := newConnLimiter(maxSSEPerUser)
	return func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		id, _ := auth.IdentityFrom(r.Context())
		admin := id != nil && id.IsPlatformAdmin
		tenantID := ""
		if id != nil {
			tenantID = id.ActiveTenantID
		}
		if id != nil && id.UserID != "" {
			if !sseConns.acquire(id.UserID) {
				http.Error(w, "too many concurrent event streams", http.StatusTooManyRequests)
				return
			}
			defer sseConns.release(id.UserID)
		}

		// Per-connection owned-VMID set (nil for admin — admin bypasses).
		var ownedSet map[int]bool
		refresh := func() {
			if admin || owned == nil || tenantID == "" {
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), ownedFetchTimeout)
			set, err := owned(ctx, tenantID)
			cancel()
			if err != nil {
				log.Warn("sse owned vmids", "err", err)
				return
			}
			ownedSet = set
		}
		refresh()

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache, no-transform")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)

		// Per-write deadline via the ResponseController (the route is exempt
		// from the global request timeout and the server sets no WriteTimeout).
		// SetWriteDeadline errors (http.ErrNotSupported through a non-Unwrap
		// wrapper, or a test recorder) are deliberately ignored — the deadline
		// is a hardening layer; the write error below is the functional signal.
		rc := http.NewResponseController(w)
		write := func(frame string) bool {
			_ = rc.SetWriteDeadline(time.Now().Add(sseWriteTimeout))
			if _, err := io.WriteString(w, frame); err != nil {
				return false // dead/stalled client — drop the connection
			}
			fl.Flush()
			return true
		}
		if !write("retry: 5000\n\n") {
			return
		}

		ch, cancel := b.Subscribe()
		defer cancel()

		hb := time.NewTicker(heartbeatInterval)
		defer hb.Stop()
		refreshTick := time.NewTicker(ownedRefreshInterval)
		defer refreshTick.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-refreshTick.C:
				refresh()
			case <-hb.C:
				if !write(": ping\n\n") {
					return
				}
			case e, open := <-ch:
				if !open {
					return
				}
				if !deliver(e, admin, ownedSet) {
					continue
				}
				payload, err := json.Marshal(e.Data)
				if err != nil {
					log.Error("sse marshal", "event", e.Name, "err", err)
					continue
				}
				if !write(fmt.Sprintf("event: %s\ndata: %s\n\n", e.Name, payload)) {
					return
				}
			}
		}
	}
}

// deliver applies the per-connection tenant scope to one event (ADR-0011).
func deliver(e Event, admin bool, owned map[int]bool) bool {
	switch e.Name {
	case "metrics":
		// Cluster node capacity is platform-admin only.
		return admin
	case "task":
		if admin {
			return true
		}
		te, ok := e.Data.(types.TaskEvent)
		if !ok || te.Resource == nil {
			return false
		}
		return owned[te.Resource.VMID]
	case "deployment":
		if admin {
			return true
		}
		dep, ok := e.Data.(*types.Deployment)
		if !ok {
			return false
		}
		return owned[dep.VMID]
	case "schedule_warning":
		// Auto-shutdown T-15m warning (ADR-0019). MUST be scoped like task frames:
		// a warning names the owning VMID and reaches only the owning tenant (and
		// platform-admin). It must NOT fall through to the default broadcast case,
		// which would leak one tenant's guest activity to every subscriber.
		if admin {
			return true
		}
		ev, ok := e.Data.(types.ScheduleWarningEvent)
		if !ok {
			return false
		}
		return owned[ev.VMID]
	case "ttl_warning":
		// TTL heads-up (T-24h / T-1h, ADR-0020). Scoped exactly like task frames:
		// it names the owning VMID and reaches only the owning tenant (and
		// platform-admin). It must NOT fall through to the default broadcast case,
		// which would leak one tenant's guest activity to every subscriber.
		if admin {
			return true
		}
		ev, ok := e.Data.(types.TtlWarningEvent)
		if !ok {
			return false
		}
		return owned[ev.VMID]
	case "deployment_set":
		// Deployment-set progress (ADR-0029). A set frame names N member VMIDs that
		// (a set being atomic) share one tenant, so it is delivered only when EVERY
		// member VMID is owned by the subscriber's active tenant; platform-admin
		// bypasses. It must NOT fall through to the default broadcast case — a set
		// frame carries a tenant's whole cluster topology, so broadcasting it would
		// leak that topology to every subscriber (the same hazard the warning frames
		// above call out). An empty/typed-mismatched payload fails closed.
		if admin {
			return true
		}
		set, ok := e.Data.(*types.DeploymentSet)
		if !ok || len(set.Members) == 0 {
			return false
		}
		for _, m := range set.Members {
			if !owned[m.VMID] {
				return false
			}
		}
		return true
	default:
		// No other frame types exist; deliver to everyone rather than silently
		// drop a future benign frame.
		return true
	}
}
