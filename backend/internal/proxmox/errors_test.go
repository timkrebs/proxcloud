package proxmox

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"syscall"
	"testing"

	goproxmox "github.com/luthermonson/go-proxmox"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

func TestMapErr(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   string
		wantStatus int
		wantPVE    string // "" = don't assert on PVEMessage
	}{
		{
			name: "nil stays nil",
			err:  nil,
		},
		{
			name:       "401 status line",
			err:        errors.New("401 authentication failure"),
			wantCode:   "proxmox_auth_failed",
			wantStatus: http.StatusBadGateway,
			wantPVE:    "401 authentication failure",
		},
		{
			name:       "lib not-authorized sentinel",
			err:        fmt.Errorf("get version: %w", goproxmox.ErrNotAuthorized),
			wantCode:   "proxmox_auth_failed",
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "403 permission check failed",
			err:        errors.New(`403 Permission check failed ("/vms/100", "VM.Audit")`),
			wantCode:   "proxmox_permission_denied",
			wantStatus: http.StatusForbidden,
			wantPVE:    `403 Permission check failed ("/vms/100", "VM.Audit")`,
		},
		{
			name:       "404 status line",
			err:        errors.New("404 Not Found"),
			wantCode:   "not_found",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "lib not-found sentinel",
			err:        goproxmox.ErrNotFound,
			wantCode:   "not_found",
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "pve 500 does-not-exist message",
			err:        errors.New(`500 storage 'local-zfs' does not exist`),
			wantCode:   "not_found",
			wantStatus: http.StatusNotFound,
			wantPVE:    `500 storage 'local-zfs' does not exist`,
		},
		{
			name:       "595 proxy status string",
			err:        errors.New("595 Errors during connection establishment, proxy handshake"),
			wantCode:   "proxmox_unreachable",
			wantStatus: http.StatusBadGateway,
			wantPVE:    "595 Errors during connection establishment, proxy handshake",
		},
		{
			name:       "connection refused",
			err:        &url.Error{Op: "Get", URL: "https://pve01:8006/api2/json/version", Err: syscall.ECONNREFUSED},
			wantCode:   "proxmox_unreachable",
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "unexpected EOF",
			err:        io.ErrUnexpectedEOF,
			wantCode:   "proxmox_unreachable",
			wantStatus: http.StatusBadGateway,
		},
		{
			name:       "context deadline exceeded",
			err:        &url.Error{Op: "Get", URL: "https://pve01:8006/api2/json/version", Err: context.DeadlineExceeded},
			wantCode:   "timeout",
			wantStatus: http.StatusGatewayTimeout,
		},
		{
			name:       "plain 500 body passthrough",
			err:        errors.New("500 hostname lookup 'pve02' failed - failed to get address info for: pve02"),
			wantCode:   "proxmox_error",
			wantStatus: http.StatusBadGateway,
			wantPVE:    "500 hostname lookup 'pve02' failed - failed to get address info for: pve02",
		},
		{
			name:       "bad request parameter verification",
			err:        errors.New("bad request: 400 Parameter verification failed. - {\"vmid\":\"invalid format\"}"),
			wantCode:   "invalid_request",
			wantStatus: http.StatusBadRequest,
			wantPVE:    "bad request: 400 Parameter verification failed. - {\"vmid\":\"invalid format\"}",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := mapErr("test op", tc.err)

			if tc.err == nil {
				if got != nil {
					t.Fatalf("mapErr(nil) = %v, want nil", got)
				}
				return
			}

			var apiErr *types.APIError
			if !errors.As(got, &apiErr) {
				t.Fatalf("mapErr returned %T (%v), want *types.APIError", got, got)
			}
			if apiErr.Code != tc.wantCode {
				t.Errorf("Code = %q, want %q (err: %v)", apiErr.Code, tc.wantCode, tc.err)
			}
			if apiErr.Status != tc.wantStatus {
				t.Errorf("Status = %d, want %d", apiErr.Status, tc.wantStatus)
			}
			if tc.wantPVE != "" && apiErr.PVEMessage != tc.wantPVE {
				t.Errorf("PVEMessage = %q, want verbatim %q", apiErr.PVEMessage, tc.wantPVE)
			}
			if apiErr.Message == "" {
				t.Error("Message is empty; every mapped error needs a safe human message")
			}
		})
	}
}

// mapErr must pass through errors that are already mapped so double-mapping
// deep call stacks cannot rewrite a specific code into proxmox_error.
func TestMapErrPassesThroughAPIError(t *testing.T) {
	orig := &types.APIError{Code: "invalid_request", Message: "bad timeframe", Status: http.StatusBadRequest}
	got := mapErr("outer op", fmt.Errorf("wrap: %w", orig))

	var apiErr *types.APIError
	if !errors.As(got, &apiErr) {
		t.Fatalf("mapErr returned %T, want *types.APIError", got)
	}
	if apiErr != orig {
		t.Errorf("mapErr rewrote an already-mapped error: got %+v, want the original", apiErr)
	}
}

func TestUPIDNode(t *testing.T) {
	tests := []struct {
		upid UPID
		want string
	}{
		{"UPID:pve01:0004C9B2:03A462AE:66B0F2E1:qmcreate:101:root@pam!proxcloud:", "pve01"},
		{"UPID:node-a:1:2:3:vzstart:200:root@pam:", "node-a"},
		{"not-a-upid", ""},
		{"UPID::0004C9B2:", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := tc.upid.Node(); got != tc.want {
			t.Errorf("UPID(%q).Node() = %q, want %q", tc.upid, got, tc.want)
		}
	}
}
