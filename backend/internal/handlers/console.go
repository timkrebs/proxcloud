package handlers

import (
	"encoding/json"
	"net/http"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/console"
	"github.com/timkrebs9/proxcloud/backend/internal/httpserver"
)

// OpenConsole serves POST /api/guests/{node}/{type}/{vmid}/console.
// Answers 503 console_disabled when the credential pair is not configured.
func (d *Deps) OpenConsole(w http.ResponseWriter, r *http.Request) {
	if d.ConsoleAuth == nil || d.ConsoleSessions == nil {
		httpserver.WriteError(w, console.ErrDisabled)
		return
	}
	ref, apiErr := guestRef(r)
	if apiErr != nil {
		httpserver.WriteError(w, apiErr)
		return
	}
	var req struct {
		Kind string `json:"kind"` // vnc | term
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Kind != "vnc" && req.Kind != "term") {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: `kind must be "vnc" or "term"`, Status: http.StatusBadRequest})
		return
	}
	if ref.Type == "lxc" && req.Kind == "vnc" {
		httpserver.WriteError(w, &types.APIError{Code: "invalid_request", Message: "containers use the terminal console", Status: http.StatusBadRequest})
		return
	}

	proxy, err := d.ConsoleAuth.CreateProxy(r.Context(), ref.Node, ref.Type, ref.VMID, req.Kind)
	if err != nil {
		httpserver.WriteError(w, err)
		return
	}
	id := d.ConsoleSessions.Create(console.Session{
		Node:      ref.Node,
		GuestType: ref.Type,
		VMID:      ref.VMID,
		Kind:      req.Kind,
		Proxy:     proxy,
		AuthUser:  d.ConsoleUser,
	})
	httpserver.WriteJSON(w, http.StatusOK, types.ConsoleSession{
		SessionID: id,
		Kind:      req.Kind,
		Password:  proxy.Password,
	})
}
