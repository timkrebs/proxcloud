package events

import (
	"context"
	"log/slog"
	"sync"
	"time"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
)

const (
	pollInterval  = 5 * time.Second
	nodeFetchTime = 5 * time.Second
)

// MetricsPoller publishes a "metrics" event every pollInterval while at
// least one SSE subscriber is connected; with no subscribers it idles
// without touching Proxmox.
type MetricsPoller struct {
	PVE    proxmox.Client
	Broker *Broker
	Log    *slog.Logger
}

// Run blocks until ctx is canceled.
func (p *MetricsPoller) Run(ctx context.Context) {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if p.Broker.SubscriberCount() == 0 {
				continue
			}
			p.tick(ctx)
		}
	}
}

func (p *MetricsPoller) tick(ctx context.Context) {
	rows, err := p.PVE.ClusterResources(ctx)
	if err != nil {
		p.Log.Warn("metrics poll: cluster resources", "err", err)
		return
	}

	var nodeNames []string
	for _, r := range rows {
		if r.Type == "node" {
			nodeNames = append(nodeNames, r.Node)
		}
	}

	metrics := make([]types.NodeMetric, len(nodeNames))
	var wg sync.WaitGroup
	for i, name := range nodeNames {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			nctx, cancel := context.WithTimeout(ctx, nodeFetchTime)
			defer cancel()
			st, err := p.PVE.NodeStatus(nctx, name)
			if err != nil {
				// Zeroes with Online=false — an honest "could not measure".
				metrics[i] = types.NodeMetric{Node: name, Online: false}
				return
			}
			metrics[i] = types.NodeMetric{
				Node:      name,
				Online:    true,
				CPUPct:    st.CPUPct,
				MemUsed:   st.MemUsed,
				MemTotal:  st.MemTotal,
				UptimeSec: st.Uptime,
			}
		}(i, name)
	}
	wg.Wait()

	p.Broker.Publish(Event{Name: "metrics", Data: types.MetricsEvent{TS: time.Now().UTC(), Nodes: metrics}})
}
