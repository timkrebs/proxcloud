package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// jsonUnmarshal decodes a 2xx body, wrapping errors with a readable snippet.
func jsonUnmarshal(b []byte, out any) error {
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode response: %w (body: %s)", err, snippet(b))
	}
	return nil
}

// baseURL parses the configured base into a *url.URL for cookie-jar lookups.
func baseURL(base string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, err
	}
	if u.Host == "" {
		return nil, fmt.Errorf("base URL %q has no host", base)
	}
	return u, nil
}

// short truncates long ids (SHAs, UPIDs, deployment ids) for readable output.
func short(s string) string {
	if len(s) <= 14 {
		return s
	}
	return s[:14] + "…"
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// urlEscape path-escapes a single path segment (a UPID contains ':' and other
// characters that must be encoded when spliced into a path).
func urlEscape(s string) string { return url.PathEscape(s) }

// mdEscape neutralizes the pipe so a detail string cannot break the summary
// table, and collapses newlines.
func mdEscape(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return s
}
