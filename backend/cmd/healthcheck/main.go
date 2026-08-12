// Command healthcheck is a tiny, dependency-free liveness probe for the
// distroless production image, which ships no shell and no wget/curl for a
// Docker HEALTHCHECK to call. It performs a single GET against the local
// /api/health endpoint and exits non-zero on any failure, so both the
// Dockerfile HEALTHCHECK and any orchestrator can gate readiness on it.
//
// The target defaults to the in-container listen port (:8080, matching the
// Dockerfile EXPOSE); override with HEALTHCHECK_URL if LISTEN_ADDR differs.
package main

import (
	"net/http"
	"os"
	"time"
)

func main() {
	url := os.Getenv("HEALTHCHECK_URL")
	if url == "" {
		url = "http://127.0.0.1:8080/api/health"
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}
