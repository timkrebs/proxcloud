package proxmox

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"

	goproxmox "github.com/luthermonson/go-proxmox"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

// Sentinel errors implementation code can return (or wrap) to force a
// specific mapping through mapErr without hand-building an APIError.
var (
	// ErrNotFound maps to not_found (404).
	ErrNotFound = errors.New("proxmox: object not found")
	// ErrUnreachable maps to proxmox_unreachable (502).
	ErrUnreachable = errors.New("proxmox: api unreachable")
)

// mapErr converts any error coming out of a Proxmox call into the stable
// *types.APIError taxonomy. op is a short, log/UI-safe description of what
// was attempted ("query cluster status"); the raw error text is surfaced
// verbatim in PVEMessage per the iron rules — it never contains secrets
// (the token travels in a header, never in URLs or PVE messages).
//
// go-proxmox surfaces PVE HTTP 500/501 as errors.New(res.Status), i.e. the
// PVE error message prefixed with the status code ("500 storage 'x' does not
// exist"), and collapses both 401 and 403 into ErrNotAuthorized. The string
// checks below therefore look at status prefixes and PVE's well-known
// phrases; the sentinel alone is treated as auth failure (with token auth a
// blanket rejection is far more often a bad token than a missing privilege,
// and explicit 403 bodies are caught before the sentinel check).
func mapErr(op string, err error) error {
	if err == nil {
		return nil
	}
	var apiErr *types.APIError
	if errors.As(err, &apiErr) {
		return apiErr // already mapped; keep the original context
	}

	msg := err.Error()
	lower := strings.ToLower(msg)

	switch {
	case errors.Is(err, context.DeadlineExceeded) || isNetTimeout(err):
		return &types.APIError{
			Code:    "timeout",
			Message: op + ": Proxmox did not answer in time",
			Status:  http.StatusGatewayTimeout,
		}

	case statusPrefix(msg, "403") || strings.Contains(lower, "permission check failed") || strings.Contains(lower, "permission denied"):
		return &types.APIError{
			Code:       "proxmox_permission_denied",
			Message:    op + ": the API token lacks the required Proxmox privileges",
			PVEMessage: msg,
			Status:     http.StatusForbidden,
		}

	case statusPrefix(msg, "401") || strings.Contains(lower, "authentication failure") || errors.Is(err, goproxmox.ErrNotAuthorized):
		return &types.APIError{
			Code:       "proxmox_auth_failed",
			Message:    op + ": Proxmox rejected the API token",
			PVEMessage: msg,
			Status:     http.StatusBadGateway,
		}

	case errors.Is(err, ErrNotFound) || errors.Is(err, goproxmox.ErrNotFound) || statusPrefix(msg, "404") || strings.Contains(lower, "does not exist"):
		return &types.APIError{
			Code:       "not_found",
			Message:    op + ": not found on Proxmox",
			PVEMessage: msg,
			Status:     http.StatusNotFound,
		}

	case errors.Is(err, ErrUnreachable) || isUnreachable(err, lower) || strings.Contains(msg, "595"):
		// 595/596 are pveproxy's "errors during connection establishment /
		// connection timed out" statuses for unreachable cluster nodes.
		return &types.APIError{
			Code:       "proxmox_unreachable",
			Message:    op + ": cannot reach the Proxmox API",
			PVEMessage: msg,
			Status:     http.StatusBadGateway,
		}

	case strings.HasPrefix(lower, "bad request:"):
		// go-proxmox wraps PVE 400 parameter-verification failures with this
		// prefix and includes the per-field errors verbatim.
		return &types.APIError{
			Code:       "invalid_request",
			Message:    op + ": Proxmox rejected the request parameters",
			PVEMessage: msg,
			Status:     http.StatusBadRequest,
		}

	default:
		return &types.APIError{
			Code:       "proxmox_error",
			Message:    op + " failed",
			PVEMessage: msg,
			Status:     http.StatusBadGateway,
		}
	}
}

// statusPrefix reports whether msg starts with the given 3-digit HTTP status
// the way go-proxmox emits status-line errors ("500 <pve message>").
func statusPrefix(msg, code string) bool {
	return msg == code || strings.HasPrefix(msg, code+" ")
}

// isNetTimeout reports network-level timeouts (net.Error.Timeout), which the
// http client wraps around dial/read deadlines.
func isNetTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// isUnreachable classifies transport-level failures: the API endpoint (or a
// proxied cluster node) cannot be talked to at all.
func isUnreachable(err error, lower string) bool {
	if errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EHOSTUNREACH) ||
		errors.Is(err, syscall.ENETUNREACH) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	return strings.Contains(lower, "connection refused") ||
		strings.Contains(lower, "connection reset") ||
		strings.Contains(lower, "no route to host") ||
		strings.Contains(lower, "network is unreachable") ||
		strings.Contains(lower, "no such host") ||
		strings.Contains(lower, "tls:") ||
		strings.Contains(lower, "x509:") ||
		strings.HasSuffix(lower, "eof")
}
