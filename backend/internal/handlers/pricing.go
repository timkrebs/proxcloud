package handlers

import (
	"net/http"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// Pricing (nil-safe) is set from config at startup; nil means disabled.
// GET /api/pricing lets the frontend decide whether to render cost UI.
func (d *Deps) GetPricing(w http.ResponseWriter, _ *http.Request) {
	if d.Pricing == nil || !d.Pricing.Enabled {
		httpserver.WriteJSON(w, http.StatusOK, types.Pricing{Enabled: false})
		return
	}
	httpserver.WriteJSON(w, http.StatusOK, *d.Pricing)
}
