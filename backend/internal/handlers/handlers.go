// Package handlers implements the authenticated REST API. Every handler
// depends only on the proxmox.Client interface — no raw HTTP, no library
// types — so table-driven tests run against proxmoxtest.MockClient. Every
// number in a response comes from Proxmox; missing data is an explicit
// error or zero-with-Online=false, never a fabricated value.
package handlers

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/deploy"
	"github.com/timkrebs9/proxcloud/backend/internal/events"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox"
	"github.com/timkrebs9/proxcloud/backend/internal/tasks"
)

// Deps carries what every handler needs. Mount attaches all routes.
// Registry and Broker are nil-safe: without them, lifecycle actions still
// work but produce no transitional overlay, notifications, or events.
type Deps struct {
	PVE      proxmox.Client
	Log      *slog.Logger
	Registry *tasks.Registry
	Broker   *events.Broker
	Deploy   *deploy.Engine
}

// Mount attaches every core REST route. It matches httpserver.Deps.Protected,
// so the caller decides the auth boundary; paths are relative to /api.
func (d *Deps) Mount(r chi.Router) {
	r.Get("/cluster", d.GetCluster)
	r.Get("/cluster/nextid", d.GetNextID)

	r.Get("/nodes", d.ListNodes)
	r.Get("/nodes/{node}", d.GetNode)
	r.Get("/nodes/{node}/metrics", d.GetNodeMetrics)
	r.Get("/nodes/{node}/bridges", d.GetNodeBridges)
	r.Get("/nodes/{node}/storages", d.GetNodeStorages)
	r.Get("/nodes/{node}/storages/{storage}/content", d.GetStorageContent)

	r.Get("/resources", d.ListResources)
	r.Get("/pools", d.ListPools)
	r.Get("/storage", d.ListStorage)

	r.Get("/guests/{node}/{type}/{vmid}", d.GetGuest)
	r.Patch("/guests/{node}/{type}/{vmid}/config", d.UpdateGuestConfig)
	r.Get("/guests/{node}/{type}/{vmid}/metrics", d.GetGuestMetrics)
	r.Get("/guests/{node}/{type}/{vmid}/interfaces", d.GetGuestInterfaces)
	r.Post("/guests/{node}/{type}/{vmid}/resize", d.ResizeGuestDisk)
	r.Get("/guests/{node}/{type}/{vmid}/snapshots", d.ListSnapshots)
	r.Post("/guests/{node}/{type}/{vmid}/snapshots", d.CreateSnapshot)
	r.Post("/guests/{node}/{type}/{vmid}/snapshots/{name}/rollback", d.RollbackSnapshot)
	r.Delete("/guests/{node}/{type}/{vmid}/snapshots/{name}", d.DeleteSnapshot)
	r.Get("/guests/{node}/{type}/{vmid}/firewall", d.GetGuestFirewall)
	r.Put("/guests/{node}/{type}/{vmid}/firewall/options", d.SetGuestFirewall)
	r.Get("/guests/{node}/{type}/{vmid}/acl", d.GetGuestACL)
	r.Post("/guests/{node}/{type}/{vmid}/{action}", d.GuestAction)
	r.Delete("/guests/{node}/{type}/{vmid}", d.DeleteGuest)

	r.Get("/tasks", d.ListTasks)
	r.Get("/tasks/{upid}", d.GetTask)
	r.Get("/tasks/{upid}/log", d.GetTaskLog)

	r.Get("/notifications", d.ListNotifications)
	r.Post("/notifications/read", d.MarkNotificationsRead)

	r.Post("/guests", d.CreateGuest)
	r.Get("/deployments/{id}", d.GetDeployment)
}

// Health probe tuning: a short deadline so /api/health stays snappy even
// when Proxmox is down, and a cache so the endpoint (often polled by
// compose/uptime checks) does not hammer PVE.
const (
	healthProbeTimeout = 3 * time.Second
	healthCacheTTL     = 30 * time.Second
)

// Health returns the public /api/health handler: the API's own liveness plus
// Proxmox reachability probed via /version and cached for healthCacheTTL.
func (d *Deps) Health() http.HandlerFunc {
	var (
		mu       sync.Mutex
		cached   string
		cachedAt time.Time
	)
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if cached == "" || time.Since(cachedAt) >= healthCacheTTL {
			ctx, cancel := context.WithTimeout(r.Context(), healthProbeTimeout)
			_, err := d.PVE.Version(ctx)
			cancel()
			if err != nil {
				cached = "unreachable"
			} else {
				cached = "ok"
			}
			cachedAt = time.Now()
		}
		pveStatus := cached
		mu.Unlock()

		// Status stays "ok": the API itself answered; Proxmox reachability
		// is reported separately so the UI can show a precise banner.
		httpserver.WriteJSON(w, http.StatusOK, types.Health{Status: "ok", Proxmox: pveStatus})
	}
}

func (d *Deps) logger() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// splitPVEList splits PVE's joined lists (tags "a;b", content "iso,backup")
// into a clean, never-nil slice. PVE canonically joins tags with semicolons
// but commas appear in the wild; both are accepted.
func splitPVEList(s string) []string {
	out := []string{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ';' || r == ',' }) {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
