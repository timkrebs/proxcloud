package types

// VersionInfo is the GET /api/v1/version response: the running binary's build
// metadata, injected at build time via the Go linker's -ldflags -X flag. This
// is public build metadata only — it carries no secrets and needs no session.
// The CD smoke test asserts the deployed commit against the expected git SHA,
// and the frontend footer renders the semver + commit.
type VersionInfo struct {
	Commit    string `json:"commit"`    // full git SHA, or "dev" when not injected
	Semver    string `json:"semver"`    // release tag (e.g. v1.2.3), or "0.0.0-dev"
	BuildTime string `json:"buildTime"` // RFC3339 build timestamp, or "unknown"
}
