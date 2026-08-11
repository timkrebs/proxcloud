package handlers

import (
	"net/http"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// ListPools serves GET /api/pools.
func (d *Deps) ListPools(w http.ResponseWriter, r *http.Request) {
	pools, err := d.PVE.Pools(r.Context())
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	if pools == nil {
		pools = []types.Pool{}
	}
	httpserver.WriteJSON(w, http.StatusOK, pools)
}
