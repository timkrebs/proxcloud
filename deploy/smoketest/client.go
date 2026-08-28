package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// maxBody bounds every response read so a misbehaving edge can never make the
// smoke binary allocate without limit.
const maxBody = 8 << 20 // 8 MiB

// apiClient is a tiny cookie-jar HTTP client for the public Proxcloud API.
// The jar carries the proxcloud_session cookie set by POST /api/auth/login
// across every subsequent request, including the SSE stream.
type apiClient struct {
	base   string
	hc     *http.Client // bounded-timeout client for request/response JSON calls
	stream *http.Client // no client-timeout; SSE liveness is bounded by context
	cfID   string       // Cloudflare Access service-token client id (optional)
	cfSec  string       // Cloudflare Access service-token client secret (optional)
}

func newAPIClient(base string, httpTimeout time.Duration, cfID, cfSec string) (*apiClient, error) {
	jar := newPermissiveJar()
	return &apiClient{
		base:   strings.TrimRight(base, "/"),
		hc:     &http.Client{Jar: jar, Timeout: httpTimeout},
		stream: &http.Client{Jar: jar}, // Timeout 0: long-lived stream, ctx-bounded
		cfID:   cfID,
		cfSec:  cfSec,
	}, nil
}

// applyAccessHeaders adds Cloudflare Access service-token headers when both are
// configured, so the smoke can reach an Access-protected origin (a gated
// qa/staging) without an interactive login. No-op when the credentials are
// empty — an un-gated origin (e.g. public prod) is unaffected.
func (c *apiClient) applyAccessHeaders(req *http.Request) {
	if c.cfID != "" && c.cfSec != "" {
		req.Header.Set("CF-Access-Client-Id", c.cfID)
		req.Header.Set("CF-Access-Client-Secret", c.cfSec)
	}
}

// permissiveJar is a minimal cookie jar for the smoke: it stores cookies per
// host and returns them regardless of the Secure attribute or request scheme.
// Prod (ADR-0015 Mode A) terminates TLS at Cloudflare, so the backend correctly
// marks proxcloud_session Secure (via X-Forwarded-Proto: https) — but the
// smoke's probe reaches the guest over a trusted, on-box PLAIN-HTTP hop, and a
// standard cookiejar drops Secure cookies on http:// requests. This carries the
// login session across the flow. Single-host, last-write-wins by name; the
// Secure-attribute correctness itself is covered by the backend's unit test, not
// relaxed here. It satisfies http.CookieJar.
type permissiveJar struct {
	mu      sync.Mutex
	cookies map[string]map[string]string // host -> name -> value
}

func newPermissiveJar() *permissiveJar {
	return &permissiveJar{cookies: map[string]map[string]string{}}
}

func (j *permissiveJar) SetCookies(u *url.URL, cs []*http.Cookie) {
	j.mu.Lock()
	defer j.mu.Unlock()
	host := u.Hostname()
	m := j.cookies[host]
	if m == nil {
		m = map[string]string{}
		j.cookies[host] = m
	}
	for _, c := range cs {
		if c.MaxAge < 0 || (c.Value == "" && !c.Expires.IsZero()) {
			delete(m, c.Name) // an expired/cleared cookie
			continue
		}
		m[c.Name] = c.Value
	}
}

func (j *permissiveJar) Cookies(u *url.URL) []*http.Cookie {
	j.mu.Lock()
	defer j.mu.Unlock()
	var out []*http.Cookie
	for name, val := range j.cookies[u.Hostname()] {
		out = append(out, &http.Cookie{Name: name, Value: val})
	}
	return out
}

// do sends an optional JSON body and returns the status code and raw response
// body. It never decodes — callers decode 2xx bodies and surface non-2xx
// bodies verbatim (honest error messages, never faked).
func (c *apiClient) do(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("encode %s %s: %w", method, path, err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	c.applyAccessHeaders(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read %s %s: %w", method, path, err)
	}
	return resp.StatusCode, data, nil
}

// getJSON does a GET and decodes a 2xx body into out. Non-2xx returns an error
// carrying the server's message.
func (c *apiClient) getJSON(ctx context.Context, path string, out any) error {
	code, body, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	if code < 200 || code >= 300 {
		return httpErr(http.MethodGet, path, code, body)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return fmt.Errorf("decode GET %s: %w (body: %s)", path, err, snippet(body))
		}
	}
	return nil
}

// httpErr renders a non-2xx response, preferring the backend's structured
// {code,message} envelope over the raw body.
func httpErr(method, path string, code int, body []byte) error {
	if ae, ok := parseAPIError(body); ok {
		return fmt.Errorf("%s %s -> HTTP %d: %s (%s)", method, path, code, ae.Message, ae.Code)
	}
	return fmt.Errorf("%s %s -> HTTP %d: %s", method, path, code, snippet(body))
}

// parseAPIError extracts the backend error envelope, tolerating both the nested
// {"error":{code,message}} shape and a flat {code,message} body.
func parseAPIError(body []byte) (apiError, bool) {
	var nested struct {
		Error apiError `json:"error"`
	}
	if json.Unmarshal(body, &nested) == nil && nested.Error.Message != "" {
		return nested.Error, true
	}
	var flat apiError
	if json.Unmarshal(body, &flat) == nil && flat.Message != "" {
		return flat, true
	}
	return apiError{}, false
}

// errorCode returns the machine-readable error code from a response body, or ""
// when the body is not a recognizable error envelope.
func errorCode(body []byte) string {
	ae, _ := parseAPIError(body)
	return ae.Code
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 240 {
		return s[:240] + "…"
	}
	return s
}

// sseResult is what the SSE liveness check observed.
type sseResult struct {
	Frames   int
	SawEvent bool   // a real "event:" frame (deployment/task) was delivered
	First    string // first non-empty SSE line, for the summary detail
}

// readSSE opens the SSE stream and reads until it has seen at least one full
// SSE frame or the context deadline fires. Receiving the immediate preamble
// (retry:) proves the proxy's buffering-off flush path (ADR-0015 §5); a real
// event: frame (delivered only for guests this tenant owns) is the richer
// signal and is noted when seen.
func (c *apiClient) readSSE(ctx context.Context, path string) (sseResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return sseResult{}, err
	}
	req.Header.Set("Accept", "text/event-stream")
	c.applyAccessHeaders(req)
	resp, err := c.stream.Do(req)
	if err != nil {
		return sseResult{}, fmt.Errorf("open SSE %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		return sseResult{}, httpErr(http.MethodGet, path, resp.StatusCode, body)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		return sseResult{}, fmt.Errorf("SSE %s: unexpected Content-Type %q (proxy not passing the stream through?)", path, ct)
	}
	return scanSSE(resp.Body)
}

// scanSSE consumes an event-stream and returns once it has counted at least one
// frame (a block terminated by a blank line). Split out for unit testing.
func scanSSE(r io.Reader) (sseResult, error) {
	br := bufio.NewReader(r)
	var res sseResult
	blockHasContent := false
	sawEventInBlock := false
	for {
		line, err := br.ReadString('\n')
		if s := strings.TrimRight(line, "\r\n"); len(line) > 0 {
			if s == "" {
				if blockHasContent {
					res.Frames++
					if sawEventInBlock {
						res.SawEvent = true
					}
				}
				blockHasContent = false
				sawEventInBlock = false
			} else {
				if !blockHasContent && res.First == "" {
					res.First = s
				}
				blockHasContent = true
				if strings.HasPrefix(s, "event:") {
					sawEventInBlock = true
				}
			}
		}
		if res.Frames >= 1 {
			return res, nil
		}
		if err != nil {
			// Deadline/close with no frame yet: report what we saw (if the
			// preamble line arrived but no terminating blank line, First is set).
			if res.First != "" {
				return res, fmt.Errorf("SSE closed after %q before a complete frame", res.First)
			}
			return res, fmt.Errorf("SSE delivered no frame before timeout: %w", err)
		}
	}
}
