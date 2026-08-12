package proxmox

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	goproxmox "github.com/luthermonson/go-proxmox"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

// writePVEStatus hijacks the connection to emit a response whose HTTP status
// line carries a custom reason phrase — exactly how pveproxy surfaces errors
// (e.g. "HTTP/1.1 500 pool 'x' already exists"). Go's standard ResponseWriter
// cannot set a custom reason phrase, and go-proxmox turns a 500 into
// errors.New(res.Status), so this is the only faithful way to exercise the
// message-text idempotency branches in pools.go.
func writePVEStatus(w http.ResponseWriter, statusLine string) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		panic("test server does not support hijacking")
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		panic(err)
	}
	defer func() { _ = conn.Close() }()
	bw := bufio.NewWriter(conn)
	fmt.Fprintf(bw, "HTTP/1.1 %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", statusLine)
	_ = bw.Flush()
}

// newPoolTestClient builds a GoPVE whose raw client points at srv. Token auth
// (not credentials) matches production and avoids go-proxmox's session path.
func newPoolTestClient(srvURL string) *GoPVE {
	return &GoPVE{c: goproxmox.NewClient(srvURL, goproxmox.WithAPIToken("proxcloud@pam!t", "secret"))}
}

func TestPoolMethods(t *testing.T) {
	tests := []struct {
		name string
		// respond writes the fake PVE reply; ok==200 empty body by default.
		respond func(w http.ResponseWriter)
		call    func(ctx context.Context, g *GoPVE) error

		wantReqs   int    // number of HTTP requests the method must make (no UPID polling)
		wantMethod string // asserted only when wantReqs > 0
		wantPath   string
		wantBody   map[string]string // JSON body fields expected (nil = assert no body)
		wantErr    bool
		wantCode   string // asserted when wantErr and non-empty
	}{
		{
			name:       "create pool success sends poolid+comment, no polling",
			respond:    func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) },
			call:       func(ctx context.Context, g *GoPVE) error { return g.CreatePool(ctx, "pc-acme-web", "hi") },
			wantReqs:   1,
			wantMethod: http.MethodPost,
			wantPath:   "/pools",
			wantBody:   map[string]string{"poolid": "pc-acme-web", "comment": "hi"},
		},
		{
			name:       "create pool omits empty comment",
			respond:    func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) },
			call:       func(ctx context.Context, g *GoPVE) error { return g.CreatePool(ctx, "pc-acme-web", "") },
			wantReqs:   1,
			wantMethod: http.MethodPost,
			wantPath:   "/pools",
			wantBody:   map[string]string{"poolid": "pc-acme-web"}, // comment key absent
		},
		{
			name:       "create pool already-exists 500 is idempotent success",
			respond:    func(w http.ResponseWriter) { writePVEStatus(w, "500 pool 'pc-acme-web' already exists") },
			call:       func(ctx context.Context, g *GoPVE) error { return g.CreatePool(ctx, "pc-acme-web", "") },
			wantReqs:   1,
			wantMethod: http.MethodPost,
			wantPath:   "/pools",
			wantBody:   map[string]string{"poolid": "pc-acme-web"},
		},
		{
			name:     "create pool other 500 surfaces mapped error",
			respond:  func(w http.ResponseWriter) { writePVEStatus(w, "500 unable to create pool - backend failure") },
			call:     func(ctx context.Context, g *GoPVE) error { return g.CreatePool(ctx, "pc-acme-web", "") },
			wantReqs: 1,
			wantErr:  true,
			wantCode: "proxmox_error",
		},
		{
			name:     "create pool 403 maps to a clear error",
			respond:  func(w http.ResponseWriter) { w.WriteHeader(http.StatusForbidden) },
			call:     func(ctx context.Context, g *GoPVE) error { return g.CreatePool(ctx, "pc-acme-web", "") },
			wantReqs: 1,
			wantErr:  true,
			// go-proxmox collapses 401/403 into ErrNotAuthorized (the body/message
			// is lost); mapErr surfaces it as an auth failure. Still a clear error.
			wantCode: "proxmox_auth_failed",
		},
		{
			name:       "delete pool success, no body, no polling",
			respond:    func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) },
			call:       func(ctx context.Context, g *GoPVE) error { return g.DeletePool(ctx, "pc-acme-web") },
			wantReqs:   1,
			wantMethod: http.MethodDelete,
			wantPath:   "/pools/pc-acme-web",
			wantBody:   nil,
		},
		{
			name:    "add pool members sends vms csv via PUT, no polling",
			respond: func(w http.ResponseWriter) { w.WriteHeader(http.StatusOK) },
			call: func(ctx context.Context, g *GoPVE) error {
				return g.AddPoolMembers(ctx, "pc-acme-web", []int{101, 102})
			},
			wantReqs:   1,
			wantMethod: http.MethodPut,
			wantPath:   "/pools/pc-acme-web",
			wantBody:   map[string]string{"vms": "101,102"},
		},
		{
			name:       "add pool members already-member 500 is idempotent success",
			respond:    func(w http.ResponseWriter) { writePVEStatus(w, "500 VM 101 is already a pool member") },
			call:       func(ctx context.Context, g *GoPVE) error { return g.AddPoolMembers(ctx, "pc-acme-web", []int{101}) },
			wantReqs:   1,
			wantMethod: http.MethodPut,
			wantPath:   "/pools/pc-acme-web",
			wantBody:   map[string]string{"vms": "101"},
		},
		{
			name:     "add pool members other 500 surfaces mapped error",
			respond:  func(w http.ResponseWriter) { writePVEStatus(w, "500 pool 'pc-acme-web' does not exist") },
			call:     func(ctx context.Context, g *GoPVE) error { return g.AddPoolMembers(ctx, "pc-acme-web", []int{101}) },
			wantReqs: 1,
			wantErr:  true,
			wantCode: "not_found", // "does not exist" maps to not_found
		},
		{
			name:     "add pool members empty vmids makes no request",
			respond:  func(w http.ResponseWriter) { t.Fatal("no HTTP request expected for empty vmids") },
			call:     func(ctx context.Context, g *GoPVE) error { return g.AddPoolMembers(ctx, "pc-acme-web", nil) },
			wantReqs: 0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var (
				reqs       int
				gotMethod  string
				gotPath    string
				gotBodyRaw []byte
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reqs++
				gotMethod = r.Method
				gotPath = r.URL.Path
				gotBodyRaw, _ = io.ReadAll(r.Body)
				tc.respond(w)
			}))
			defer srv.Close()

			g := newPoolTestClient(srv.URL)
			err := tc.call(context.Background(), g)

			if reqs != tc.wantReqs {
				t.Fatalf("HTTP requests = %d, want %d (a synchronous pool call must not poll a UPID)", reqs, tc.wantReqs)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got nil")
				}
				var apiErr *types.APIError
				if !errors.As(err, &apiErr) {
					t.Fatalf("error %T (%v), want *types.APIError", err, err)
				}
				if tc.wantCode != "" && apiErr.Code != tc.wantCode {
					t.Fatalf("error code = %q, want %q (err: %v)", apiErr.Code, tc.wantCode, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.wantReqs == 0 {
				return
			}
			if gotMethod != tc.wantMethod {
				t.Errorf("method = %s, want %s", gotMethod, tc.wantMethod)
			}
			if gotPath != tc.wantPath {
				t.Errorf("path = %s, want %s", gotPath, tc.wantPath)
			}
			assertBody(t, gotBodyRaw, tc.wantBody)
		})
	}
}

// assertBody checks the JSON request body. want==nil asserts an empty body;
// otherwise every want key must be present with the given value and no extra
// keys may appear (so an omitted-comment case is genuinely omitted).
func assertBody(t *testing.T, raw []byte, want map[string]string) {
	t.Helper()
	if want == nil {
		if len(raw) != 0 {
			t.Errorf("expected no request body, got %q", string(raw))
		}
		return
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("body %q is not JSON: %v", string(raw), err)
	}
	if len(got) != len(want) {
		t.Errorf("body keys = %v, want exactly %v", got, want)
	}
	for k, v := range want {
		gv, ok := got[k]
		if !ok {
			t.Errorf("body missing key %q (got %v)", k, got)
			continue
		}
		if fmt.Sprintf("%v", gv) != v {
			t.Errorf("body[%q] = %v, want %q", k, gv, v)
		}
	}
}

func TestPoolIdempotencyClassifiers(t *testing.T) {
	tests := []struct {
		name           string
		err            error
		wantExists     bool
		wantPoolMember bool
	}{
		{"nil error", nil, false, false},
		{"already exists lowercase", errors.New("500 pool 'x' already exists"), true, false},
		{"already exists mixed case", errors.New("500 Pool ALREADY EXISTS"), true, false},
		{"already a pool member", errors.New("500 VM 101 is already a pool member"), false, true},
		{"unrelated 500", errors.New("500 backend failure"), false, false},
		{"does not exist", errors.New("500 pool 'x' does not exist"), false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAlreadyExists(tc.err); got != tc.wantExists {
				t.Errorf("isAlreadyExists(%v) = %v, want %v", tc.err, got, tc.wantExists)
			}
			if got := isAlreadyPoolMember(tc.err); got != tc.wantPoolMember {
				t.Errorf("isAlreadyPoolMember(%v) = %v, want %v", tc.err, got, tc.wantPoolMember)
			}
		})
	}
}
