package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
	"github.com/timkrebs9/proxcloud/backend/internal/version"
)

// TestVersionHandler asserts GET /api/v1/version returns 200 with the three
// build-metadata fields sourced from version.Info(). The defaults are fine —
// the point is the wire shape (commit/semver/buildTime) the CD smoke test and
// the frontend footer depend on. A second case overrides the package vars to
// prove the handler serializes injected values verbatim (the -ldflags path).
func TestVersionHandler(t *testing.T) {
	tests := []struct {
		name string
		// setup, when non-nil, overrides the version vars and returns a restore
		// func; nil exercises the shipped defaults.
		setup func(t *testing.T)
		want  types.VersionInfo
	}{
		{
			name: "defaults (un-injected build)",
			want: types.VersionInfo{Commit: "dev", Semver: "0.0.0-dev", BuildTime: "unknown"},
		},
		{
			name: "injected values (simulates -ldflags -X)",
			setup: func(t *testing.T) {
				prevC, prevS, prevB := version.Commit, version.Semver, version.BuildTime
				t.Cleanup(func() { version.Commit, version.Semver, version.BuildTime = prevC, prevS, prevB })
				version.Commit = "abc123def4567890abc123def4567890abc12345"
				version.Semver = "v1.2.3"
				version.BuildTime = "2026-08-12T00:00:00Z"
			},
			want: types.VersionInfo{
				Commit:    "abc123def4567890abc123def4567890abc12345",
				Semver:    "v1.2.3",
				BuildTime: "2026-08-12T00:00:00Z",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.setup != nil {
				tc.setup(t)
			}

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
			versionHandler(rec, req)

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}

			var got types.VersionInfo
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatalf("decode body %q: %v", rec.Body.String(), err)
			}
			if got != tc.want {
				t.Errorf("VersionInfo = %+v, want %+v", got, tc.want)
			}
			if got.Commit == "" || got.Semver == "" || got.BuildTime == "" {
				t.Errorf("all three fields must be non-empty, got %+v", got)
			}
		})
	}
}
