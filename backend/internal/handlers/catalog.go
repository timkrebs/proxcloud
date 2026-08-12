package handlers

import (
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// ListCatalogNodes serves GET /api/tenants/{tenantId}/catalog/nodes: placeable
// node NAMES only — no capacity detail for non-admins (ADR-0007 §4). Node
// placement stays tenant-chosen by name.
func (d *Deps) ListCatalogNodes(w http.ResponseWriter, r *http.Request) {
	rows, err := d.PVE.ClusterResources(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	seen := map[string]bool{}
	out := []types.CatalogNode{}
	for _, row := range rows {
		if row.Type != "node" || strings.ToLower(row.Status) != "online" {
			continue
		}
		if seen[row.Node] {
			continue
		}
		seen[row.Node] = true
		out = append(out, types.CatalogNode{Name: row.Node})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	httpserver.WriteJSON(w, http.StatusOK, out)
}

// ListCatalogStorages serves
// GET /api/tenants/{tenantId}/catalog/nodes/{node}/storages?content=…: storage
// id + content types only. Free/total capacity is stripped for non-admins.
func (d *Deps) ListCatalogStorages(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "node")
	if !ValidPVEID(node) {
		httpserver.WriteError(w, notFound("invalid node name"))
		return
	}
	storages, err := d.PVE.NodeStorages(r.Context(), node, r.URL.Query().Get("content"))
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	// Project to id + content (+ plugin type) only — free/total capacity is
	// omitted entirely, never emitted as a fabricated zero.
	out := []types.CatalogStorage{}
	for _, s := range storages {
		out = append(out, types.CatalogStorage{
			Storage: s.Storage,
			Type:    s.Type,
			Content: s.Content,
		})
	}
	httpserver.WriteJSON(w, http.StatusOK, out)
}
