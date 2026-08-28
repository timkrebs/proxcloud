// Command smoketest is the CD wave's black-box liveness gate (ADR-0016).
//
// It runs against a single BASE_URL — staging's origin (blocking; a failure
// stops the wave before the production gate) or the public prod URL (a failure
// triggers automatic rollback). It is NOT a test suite: it answers exactly one
// question — "is *this* deployed build serving real traffic correctly,
// end-to-end, against a real Proxmox path?" — via, in order:
//
//  1. version   GET /api/v1/version  == the deployed ref (.commit or .semver)
//  2. login     POST /api/auth/login (session cookie) as the seeded smoke user
//  3. resources GET the smoke tenant's resources (tenant-scoped read + authz)
//  4. lifecycle create a throwaway LXC → poll to done → delete → poll to gone
//  5. sse       GET /api/events delivers ≥1 SSE frame (proxy flush path)
//
// Cleanup (delete the throwaway LXC) always runs, even on partial failure, so
// neither environment is littered. Exit code is the contract: non-zero on any
// failed assertion. A per-assertion pass/fail table is printed and, when
// GITHUB_STEP_SUMMARY is set, appended there for the job summary (ADR-0014 §6).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	sha40Re  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	semverRe = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
	// nameRe mirrors the backend's guest-name rule so the throwaway LXC name
	// (and its delete confirmation) is always accepted.
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
)

// Config is the smoke run's inputs. Every field has an env var; the most useful
// also have a flag override (flag wins when non-empty).
type Config struct {
	BaseURL   string
	Email     string
	Password  string
	ExpectRef string // deployed ref: 40-hex SHA or vX.Y.Z (empty => version must be non-empty only)

	TenantRef  string // smoke tenant id or slug
	ProjectRef string // smoke project id or slug
	Node       string // target Proxmox node for the LXC create
	Template   string // vztmpl (container template) volume id
	Storage    string // storage pool for the rootfs
	Bridge     string // network bridge (default vmbr0)
	VMID       int    // reserved smoke VMID

	HTTPTimeout time.Duration
	TaskTimeout time.Duration
	SSETimeout  time.Duration

	SummaryFile string // GITHUB_STEP_SUMMARY, if set

	// Cloudflare Access service-token creds (optional). When set, every request
	// carries CF-Access-Client-Id/Secret so the smoke can reach an Access-gated
	// qa/staging origin without an interactive login. Empty => not gated.
	CFClientID     string // CF_ACCESS_CLIENT_ID
	CFClientSecret string // CF_ACCESS_CLIENT_SECRET
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envDur(key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

func loadConfig() (Config, error) {
	c := Config{
		HTTPTimeout: envDur("SMOKE_HTTP_TIMEOUT", 15*time.Second),
		TaskTimeout: envDur("SMOKE_TASK_TIMEOUT", 180*time.Second),
		SSETimeout:  envDur("SMOKE_SSE_TIMEOUT", 20*time.Second),
		SummaryFile: strings.TrimSpace(os.Getenv("GITHUB_STEP_SUMMARY")),

		CFClientID:     envOr("CF_ACCESS_CLIENT_ID", ""),
		CFClientSecret: envOr("CF_ACCESS_CLIENT_SECRET", ""),
	}
	var vmid string
	flag.StringVar(&c.BaseURL, "base-url", envOr("SMOKE_BASE_URL", ""), "base URL of the deployed origin, e.g. https://staging.proxcloud.lab")
	flag.StringVar(&c.Email, "email", envOr("SMOKE_EMAIL", ""), "seeded smoke user email (SMOKE_EMAIL)")
	flag.StringVar(&c.Password, "password", envOr("SMOKE_PASSWORD", ""), "seeded smoke user password (SMOKE_PASSWORD)")
	flag.StringVar(&c.ExpectRef, "expect-ref", envOr("SMOKE_EXPECT_REF", ""), "deployed ref to assert: 40-hex SHA or vX.Y.Z")
	flag.StringVar(&c.TenantRef, "tenant", envOr("SMOKE_TENANT", "smoke"), "smoke tenant id or slug")
	flag.StringVar(&c.ProjectRef, "project", envOr("SMOKE_PROJECT", "smoke"), "smoke project id or slug")
	flag.StringVar(&c.Node, "node", envOr("SMOKE_NODE", ""), "target Proxmox node for the LXC create")
	flag.StringVar(&c.Template, "template", envOr("SMOKE_TEMPLATE", ""), "vztmpl volume id, e.g. local:vztmpl/debian-12-standard_12.2-1_amd64.tar.zst")
	flag.StringVar(&c.Storage, "storage", envOr("SMOKE_STORAGE", ""), "storage pool for the LXC rootfs")
	flag.StringVar(&c.Bridge, "bridge", envOr("SMOKE_BRIDGE", "vmbr0"), "network bridge")
	flag.StringVar(&vmid, "vmid", envOr("SMOKE_VMID", ""), "reserved smoke VMID")
	flag.Parse()

	if vmid != "" {
		n, err := strconv.Atoi(vmid)
		if err != nil {
			return c, fmt.Errorf("SMOKE_VMID/-vmid %q is not an integer", vmid)
		}
		c.VMID = n
	}
	return c, c.validate()
}

func (c Config) validate() error {
	var missing []string
	req := map[string]string{
		"SMOKE_BASE_URL": c.BaseURL,
		"SMOKE_EMAIL":    c.Email,
		"SMOKE_PASSWORD": c.Password,
		"SMOKE_NODE":     c.Node,
		"SMOKE_TEMPLATE": c.Template,
		"SMOKE_STORAGE":  c.Storage,
	}
	for k, v := range req {
		if v == "" {
			missing = append(missing, k)
		}
	}
	if c.VMID < 100 {
		missing = append(missing, "SMOKE_VMID (>=100)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config: %s", strings.Join(missing, ", "))
	}
	if c.ExpectRef != "" && !sha40Re.MatchString(c.ExpectRef) && !semverRe.MatchString(c.ExpectRef) {
		return fmt.Errorf("SMOKE_EXPECT_REF %q is neither a 40-hex SHA nor vX.Y.Z", c.ExpectRef)
	}
	return nil
}

// versionField returns the /api/v1/version field to assert and the wanted value
// for a given deployed ref: .commit for a 40-hex SHA, .semver for a vX.Y.Z tag.
func versionField(ref string) (field, want string) {
	if semverRe.MatchString(ref) {
		return "semver", ref
	}
	return "commit", ref
}

func guestName(vmid int) string {
	return fmt.Sprintf("smoke-%d", vmid)
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "smoketest: %v\n", err)
		os.Exit(2)
	}

	r, err := newRunner(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "smoketest: %v\n", err)
		os.Exit(2)
	}

	ctx := context.Background()
	ok := r.run(ctx)
	r.report()

	if !ok {
		os.Exit(1)
	}
}
