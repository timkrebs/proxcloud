// Package version exposes the running binary's build metadata — the git
// commit, the semver tag, and the build timestamp — captured at link time via
// the Go linker's -ldflags -X flag.
//
// Devops/CI must inject the three vars below at these EXACT fully-qualified
// symbol paths (module github.com/timkrebs9/proxcloud/backend). Wire them into
// the Dockerfile / CD build verbatim:
//
//	go build -ldflags "\
//	  -X github.com/timkrebs9/proxcloud/backend/internal/version.Commit=$GIT_SHA \
//	  -X github.com/timkrebs9/proxcloud/backend/internal/version.Semver=$GIT_TAG \
//	  -X github.com/timkrebs9/proxcloud/backend/internal/version.BuildTime=$BUILD_TIME" \
//	  ./cmd/proxcloud
//
// Constraints for -X (these are linker limitations, not ours):
//   - Only top-level string vars can be set, and only at their fully-qualified
//     symbol path — do not rename or move these vars without updating CI.
//   - The value must contain no spaces (the linker tokenizes on whitespace).
//     Use an RFC3339 timestamp with no spaces, e.g. 2026-08-12T00:00:00Z, and a
//     full 40-char git SHA (git rev-parse HEAD).
//   - Any flag left unset keeps the safe default below, so a plain, un-injected
//     `go build` still runs and reports "dev" / "0.0.0-dev" / "unknown".
package version

// Build metadata, overwritten at link time via -ldflags -X. These are plain,
// addressable, top-level string vars on purpose: the -X flag can only set that
// shape, and only at the fully-qualified symbol path documented above.
var (
	// Commit is the full git SHA of the built tree ("dev" when not injected).
	Commit = "dev"
	// Semver is the release tag, e.g. v1.2.3 ("0.0.0-dev" when not injected).
	Semver = "0.0.0-dev"
	// BuildTime is the RFC3339 build timestamp ("unknown" when not injected).
	BuildTime = "unknown"
)

// BuildInfo is an immutable snapshot of the link-time build metadata.
type BuildInfo struct {
	Commit    string
	Semver    string
	BuildTime string
}

// Info returns the build metadata captured at link time. It reads the current
// values of the package vars, so a test may override them to exercise mapping.
func Info() BuildInfo {
	return BuildInfo{Commit: Commit, Semver: Semver, BuildTime: BuildTime}
}
