package events

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

const heartbeatInterval = 25 * time.Second

// Handler serves GET /api/events as a Server-Sent-Events stream. The route
// must be mounted outside the global request-timeout middleware.
func Handler(b *Broker, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

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

		for {
			select {
			case <-r.Context().Done():
				return
			case <-hb.C:
				fmt.Fprint(w, ": ping\n\n")
				fl.Flush()
			case e, open := <-ch:
				if !open {
					return
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
