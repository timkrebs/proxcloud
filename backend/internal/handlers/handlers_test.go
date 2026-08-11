package handlers_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/handlers"
	"github.com/timkrebs9/proxcloud/backend/internal/proxmox/proxmoxtest"
)

// doGet mounts the handlers on a fresh chi router (as main.go does under
// /api) and performs one GET against it.
func doGet(t *testing.T, mock *proxmoxtest.MockClient, target string) *httptest.ResponseRecorder {
	t.Helper()
	d := &handlers.Deps{
		PVE: mock,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) { d.Mount(r) })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// decodeBody decodes the recorded JSON body into T, failing the test with
// the raw body on mismatch.
func decodeBody[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode response: %v (body %q)", err, rec.Body.String())
	}
	return v
}

// wantErrorEnvelope asserts status code and the stable error code in the
// standard envelope.
func wantErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, wantCode string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body %q)", rec.Code, wantStatus, rec.Body.String())
	}
	env := decodeBody[types.ErrorEnvelope](t, rec)
	if env.Error.Code != wantCode {
		t.Fatalf("error code = %q, want %q (body %q)", env.Error.Code, wantCode, rec.Body.String())
	}
}

// pveUnreachable is the error the real client returns when the Proxmox API
// cannot be reached, exactly as the error mapper produces it.
func pveUnreachable() *types.APIError {
	return &types.APIError{
		Code:       "proxmox_unreachable",
		Message:    "query: cannot reach the Proxmox API",
		PVEMessage: "connection refused",
		Status:     http.StatusBadGateway,
	}
}
