package tasks

import (
	"context"
	"log/slog"
	"strings"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
)

const (
	watchInterval  = 2 * time.Second
	statusFetchTTL = 5 * time.Second
)

// Watcher polls the registry's running tasks and publishes task events on
// completion. Only Proxcloud-initiated tasks are polled — typically 0-3 at
// a time — so the load on PVE is negligible.
type Watcher struct {
	PVE      proxmox.Client
	Registry *Registry
	Broker   *events.Broker
	Log      *slog.Logger
}

// Run blocks until ctx is canceled.
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(watchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

func (w *Watcher) tick(ctx context.Context) {
	for _, upid := range w.Registry.Running() {
		sctx, cancel := context.WithTimeout(ctx, statusFetchTTL)
		info, err := w.PVE.TaskStatus(sctx, upid)
		cancel()
		if err != nil {
			// Transient fetch failure: leave the task tracked; the next tick
			// retries. Real task failure is signaled via ExitStatus below.
			w.Log.Warn("task watch", "upid", upid, "err", err)
			continue
		}
		if info.EndTime == 0 {
			continue // still running
		}

		succeeded := strings.EqualFold(info.ExitStatus, "OK") ||
			strings.HasPrefix(strings.ToLower(info.ExitStatus), "warnings:")
		tr := w.Registry.Complete(upid, succeeded, info.ExitStatus)
		if tr == nil {
			continue
		}
		status := "succeeded"
		if !succeeded {
			status = "failed"
		}
		w.Log.Info("task finished", "upid", upid, "action", tr.Action, "status", status)
		w.Broker.Publish(events.Event{Name: "task", Data: types.TaskEvent{
			UPID:       string(upid),
			Action:     tr.Action,
			Status:     status,
			ExitStatus: info.ExitStatus,
			Resource:   &tr.Resource,
		}})
	}
}
