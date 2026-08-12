package events

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
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
func Handler(b *Broker, log *slog.Logger, owned OwnedVMIDsFunc) http.HandlerFunc {
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
		fmt.Fprint(w, "retry: 5000\n\n")
		fl.Flush()

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
				fmt.Fprint(w, ": ping\n\n")
				fl.Flush()
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
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Name, payload)
				fl.Flush()
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
	default:
		// No other frame types exist; deliver to everyone rather than silently
		// drop a future benign frame.
		return true
	}
}
