package httpserver

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("encode response", "err", err)
	}
}

// WriteError writes err as the standard error envelope. Non-APIError values
// become an opaque 500 so internal details never leak to the client.
func WriteError(w http.ResponseWriter, err error) {
	var apiErr *types.APIError
	if !errors.As(err, &apiErr) {
		slog.Error("internal error", "err", err)
		apiErr = &types.APIError{Code: "internal", Message: "internal server error", Status: http.StatusInternalServerError}
	}
	status := apiErr.Status
	if status == 0 {
		status = http.StatusInternalServerError
	}
	WriteJSON(w, status, types.ErrorEnvelope{Error: *apiErr})
}
