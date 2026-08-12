package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVersionField(t *testing.T) {
	tests := []struct {
		name      string
		ref       string
		wantField string
		wantVal   string
	}{
		{"sha", "0123456789abcdef0123456789abcdef01234567", "commit", "0123456789abcdef0123456789abcdef01234567"},
		{"semver", "v1.4.0", "semver", "v1.4.0"},
		{"empty defaults to commit", "", "commit", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			field, want := versionField(tc.ref)
			if field != tc.wantField || want != tc.wantVal {
				t.Fatalf("versionField(%q) = (%q,%q), want (%q,%q)", tc.ref, field, want, tc.wantField, tc.wantVal)
			}
		})
	}
}

func TestConfigValidate(t *testing.T) {
	base := Config{
		BaseURL: "https://x", Email: "e", Password: "p",
		Node: "pve01", Template: "local:vztmpl/x.tar.zst", Storage: "local-lvm", VMID: 99000,
	}
	if err := base.validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}

	if err := (Config{}).validate(); err == nil {
		t.Fatal("empty config should be rejected")
	}

	bad := base
	bad.ExpectRef = "not-a-ref"
	if err := bad.validate(); err == nil {
		t.Fatal("invalid expect-ref should be rejected")
	}

	lowVMID := base
	lowVMID.VMID = 50
	if err := lowVMID.validate(); err == nil {
		t.Fatal("VMID < 100 should be rejected")
	}
}

func TestGuestNameMatchesBackendRule(t *testing.T) {
	if got := guestName(99000); got != "smoke-99000" {
		t.Fatalf("guestName = %q", got)
	}
	if !nameRe.MatchString(guestName(99999)) {
		t.Fatalf("derived name %q fails the backend name rule", guestName(99999))
	}
}

func TestScanSSE(t *testing.T) {
	tests := []struct {
		name       string
		stream     string
		wantFrames int
		wantEvent  bool
		wantErr    bool
	}{
		{
			name:       "preamble frame",
			stream:     "retry: 5000\n\n",
			wantFrames: 1,
		},
		{
			name:       "heartbeat comment frame",
			stream:     ": ping\n\n",
			wantFrames: 1,
		},
		{
			name:       "real event frame",
			stream:     "event: deployment\ndata: {\"vmid\":99000}\n\n",
			wantFrames: 1,
			wantEvent:  true,
		},
		{
			name:    "incomplete block, no terminating blank line",
			stream:  "retry: 5000\n",
			wantErr: true,
		},
		{
			name:    "empty stream",
			stream:  "",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := scanSSE(strings.NewReader(tc.stream))
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got frames=%d", res.Frames)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Frames != tc.wantFrames {
				t.Fatalf("frames = %d, want %d", res.Frames, tc.wantFrames)
			}
			if res.SawEvent != tc.wantEvent {
				t.Fatalf("sawEvent = %v, want %v", res.SawEvent, tc.wantEvent)
			}
		})
	}
}

// fakeBackend is a tiny httptest server covering the read-only assertions
// (version, login, /me, resources) so run-level behavior is exercised without a
// live environment. The Proxmox-touching lifecycle (create/delete) is not
// mocked here — that path is only real against staging/prod.
func fakeBackend(t *testing.T, commit string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, versionInfo{Commit: commit, Semver: "0.0.0-dev", BuildTime: "unknown"})
	})
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, r *http.Request) {
		var req loginRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Email != "smoke@proxcloud.local" || req.Password != "hunter2" {
			writeJSON(w, 401, apiError{Code: "unauthenticated", Message: "Invalid credentials.", Status: 401})
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "proxcloud_session", Value: "sess-abc", Path: "/"})
		writeJSON(w, 200, loginResponse{TotpRequired: false})
	})
	mux.HandleFunc("/api/auth/me", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, meResponse{Email: "smoke@proxcloud.local", Tenants: []tenantMembership{
			{ID: "tid-1", Name: "Smoke", Slug: "smoke", Role: "contributor"},
		}})
	})
	mux.HandleFunc("/api/tenants/tid-1/resources", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, []guestSummary{})
	})
	return httptest.NewServer(mux)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func newTestRunner(t *testing.T, base, expectRef string) *Runner {
	t.Helper()
	cfg := Config{
		BaseURL: base, Email: "smoke@proxcloud.local", Password: "hunter2",
		ExpectRef: expectRef, TenantRef: "smoke", ProjectRef: "smoke",
		Node: "pve01", Template: "local:vztmpl/x.tar.zst", Storage: "local-lvm", Bridge: "vmbr0",
		VMID: 99000, HTTPTimeout: 5 * time.Second, TaskTimeout: 5 * time.Second, SSETimeout: time.Second,
	}
	r, err := newRunner(cfg)
	if err != nil {
		t.Fatalf("newRunner: %v", err)
	}
	return r
}

func TestReadOnlyAssertionsPass(t *testing.T) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	srv := fakeBackend(t, commit)
	defer srv.Close()

	r := newTestRunner(t, srv.URL, commit)
	ctx := context.Background()
	for _, step := range []func(context.Context) bool{r.checkVersion, r.checkLogin, r.resolveTenant, r.checkListResources} {
		if !step(ctx) {
			t.Fatalf("assertion failed: %+v", r.results[len(r.results)-1])
		}
	}
	if r.tenantID != "tid-1" {
		t.Fatalf("tenant not resolved from slug: %q", r.tenantID)
	}
}

func TestVersionMismatchFails(t *testing.T) {
	srv := fakeBackend(t, "1111111111111111111111111111111111111111")
	defer srv.Close()

	r := newTestRunner(t, srv.URL, "2222222222222222222222222222222222222222")
	if r.checkVersion(context.Background()) {
		t.Fatal("version check should fail on SHA mismatch")
	}
	if r.results[0].Pass {
		t.Fatal("mismatch must be recorded as a failure")
	}
}

func TestLoginRejectsTOTPUser(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, _ *http.Request) {
		// A TOTP-enabled user gets 200 with totpRequired and NO session cookie.
		writeJSON(w, 200, loginResponse{TotpRequired: true})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := newTestRunner(t, srv.URL, "")
	if r.checkLogin(context.Background()) {
		t.Fatal("login must fail when the smoke user has TOTP enabled")
	}
}
