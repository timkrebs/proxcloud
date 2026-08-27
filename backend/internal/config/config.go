// Package config loads all Proxcloud configuration from environment
// variables (optionally seeded from a .env file for native development).
package config

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// Config holds every runtime setting. Secrets never leave this struct
// via logs; String/Format are deliberately not implemented.
type Config struct {
	// Proxmox connection (API token — read/write API, no websockets)
	ProxmoxURL         string // base URL, e.g. https://pve01.example:8006 (no /api2/json suffix)
	ProxmoxTokenID     string // user@realm!tokenname
	ProxmoxTokenSecret string
	ProxmoxTLSInsecure bool

	// Optional credentials for console websockets — PVE vncwebsocket
	// rejects API-token auth, so noVNC/xterm need a ticket login.
	// Console features are disabled when unset.
	ConsoleUser     string
	ConsolePassword string

	// Portal auth
	AdminUser         string
	AdminPasswordHash string // bcrypt; if empty, AdminPassword is hashed at boot (dev convenience)
	AdminPassword     string

	// Datastore & multi-tenancy (Phase 1). DatabaseURL is the Postgres DSN;
	// SecretsKey (32 bytes) encrypts secrets at rest (TOTP, later phases).
	// Session TTLs and the reconciler interval are consumed by later phases
	// but validated/parsed here so misconfiguration fails fast at boot.
	DatabaseURL        string
	SecretsKey         []byte
	SessionIdleTTL     time.Duration
	SessionAbsoluteTTL time.Duration
	ReconcilerInterval time.Duration
	// ReservationTTL bounds how long a pending create reservation may sit before
	// the reconciler reclaims it (frees the quota a backend that died mid-create
	// leaked). Default 45m > the 30m clone stepTimeout + margin (ADR-0012 §2.3).
	ReservationTTL time.Duration

	// Job scheduler & lifecycle features (ADR-0018/0019/0020). All three flags
	// default OFF (opt-in): a half-landed or misbehaving feature is inert until
	// explicitly enabled on the live deployment. AutoShutdown and TTL each also
	// require SchedulerEnabled — their workers/routes register only when both
	// their own flag AND the scheduler are on (see SchedulerFeatures predicates).
	SchedulerEnabled    bool
	AutoShutdownEnabled bool
	TTLEnabled          bool
	// SchedulerInterval is the scheduler's claim-tick period (how often it polls
	// for due jobs). Default 30s. A non-positive value disables the loop (logged).
	SchedulerInterval time.Duration
	// AutoShutdownDefaultGrace is how long a scheduled shutdown waits after the
	// graceful ACPI request before force-stopping a guest that has not powered
	// off. Default 120s; a per-schedule grace_seconds overrides it.
	AutoShutdownDefaultGrace time.Duration

	// Outbound email (Phase 5 invitations, ADR-0013). When SMTPHost is empty the
	// server uses the dev LogMailer (prints the accept link to stdout); when set,
	// an SMTPMailer delivers real mail. Credentials are never logged. If SMTPHost
	// is set, SMTPFrom is required.
	SMTPHost     string
	SMTPPort     string // default 587
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
	SMTPStartTLS bool // default true

	// Phase 5 security lifetimes (ADR-0013), validated at boot.
	InvitationTTL     time.Duration // default 72h
	LoginChallengeTTL time.Duration // default 5m
	TOTPIssuer        string        // otpauth issuer label; default "Proxcloud"

	// Optional flat-rate pricing; all cost UI is hidden when unset.
	PricingCurrency    string
	PricingVCPUMonth   float64
	PricingRAMGBMonth  float64
	PricingDiskGBMonth float64

	// FrontendOrigin, when set, is enforced as the only allowed Origin on
	// state-changing requests and the console websocket.
	FrontendOrigin string

	// InsecureCookies drops the cookie Secure attribute — explicit dev-only
	// opt-out (modern browsers accept Secure cookies on http://localhost).
	InsecureCookies bool

	// TrustProxyHeaders honors X-Forwarded-Proto from the reverse proxy when
	// deciding the cookie Secure attribute. Production ALWAYS runs behind a
	// single trusted proxy (Caddy, ADR-0015) that terminates/represents the
	// external TLS and overwrites X-Forwarded-Proto (a client value cannot leak
	// through), so the backend sees a plain-HTTP hop yet must set Secure when the
	// external connection is HTTPS (prod Mode A: Caddy :80 behind a Cloudflare
	// Tunnel). Defaults on in production, off in dev/direct. Same trust the
	// RealIP middleware already places in the proxy's X-Forwarded-For.
	TrustProxyHeaders bool

	ListenAddr string
	Dev        bool
}

// Load reads configuration, collecting every problem so a misconfigured
// deployment fails fast with the full list instead of one var at a time.
func Load() (*Config, error) {
	cfg := &Config{
		ProxmoxURL:         strings.TrimSuffix(strings.TrimRight(os.Getenv("PROXMOX_URL"), "/"), "/api2/json"),
		ProxmoxTokenID:     os.Getenv("PROXMOX_TOKEN_ID"),
		ProxmoxTokenSecret: os.Getenv("PROXMOX_TOKEN_SECRET"),
		ProxmoxTLSInsecure: os.Getenv("PROXMOX_TLS_INSECURE") == "true",
		ConsoleUser:        os.Getenv("PROXMOX_CONSOLE_USER"),
		ConsolePassword:    os.Getenv("PROXMOX_CONSOLE_PASSWORD"),
		AdminUser:          os.Getenv("ADMIN_USER"),
		AdminPasswordHash:  os.Getenv("ADMIN_PASSWORD_HASH"),
		AdminPassword:      os.Getenv("ADMIN_PASSWORD"),
		PricingCurrency:    os.Getenv("PRICING_CURRENCY"),
		FrontendOrigin:     strings.TrimRight(os.Getenv("FRONTEND_ORIGIN"), "/"),
		InsecureCookies:    os.Getenv("PROXCLOUD_INSECURE_COOKIES") == "true",
		ListenAddr:         envOr("LISTEN_ADDR", ":8080"),
		Dev:                os.Getenv("PROXCLOUD_ENV") != "production",
		DatabaseURL:        os.Getenv("DATABASE_URL"),
		SMTPHost:           strings.TrimSpace(os.Getenv("SMTP_HOST")),
		SMTPPort:           envOr("SMTP_PORT", "587"),
		SMTPUsername:       os.Getenv("SMTP_USERNAME"),
		SMTPPassword:       os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:           strings.TrimSpace(os.Getenv("SMTP_FROM")),
		SMTPStartTLS:       envBool("SMTP_STARTTLS", true),
		TOTPIssuer:         envOr("TOTP_ISSUER", "Proxcloud"),
	}
	// Behind Caddy in production (ADR-0015), so trust the proxy's forwarded
	// scheme by default there; explicit override via TRUST_PROXY_HEADERS.
	cfg.TrustProxyHeaders = envBool("TRUST_PROXY_HEADERS", !cfg.Dev)
	// Dev-only convenience default so `go run` works without a compose file;
	// in production DATABASE_URL must be set explicitly (fail-closed below).
	if cfg.DatabaseURL == "" && cfg.Dev {
		cfg.DatabaseURL = "postgres://proxcloud:proxcloud@localhost:5432/proxcloud?sslmode=disable"
	}

	var problems []string
	if cfg.ProxmoxURL == "" {
		problems = append(problems, "PROXMOX_URL is required")
	} else if u, err := url.Parse(cfg.ProxmoxURL); err != nil || u.Scheme == "" || u.Host == "" {
		problems = append(problems, "PROXMOX_URL must be an absolute URL like https://host:8006")
	}
	if cfg.ProxmoxTokenID == "" {
		problems = append(problems, "PROXMOX_TOKEN_ID is required (user@realm!tokenname)")
	}
	if cfg.ProxmoxTokenSecret == "" {
		problems = append(problems, "PROXMOX_TOKEN_SECRET is required")
	}
	// ADMIN_* is now optional: since Phase 2, identities live in Postgres.
	// When set on a fresh database it seeds a one-time platform admin (env-admin
	// cutover); a fresh install with no ADMIN_* uses the first-run bootstrap
	// endpoint instead. If configured it must be a complete pair, so a
	// half-specified admin fails fast rather than silently not seeding.
	adminPasswordSet := cfg.AdminPasswordHash != "" || cfg.AdminPassword != ""
	if cfg.AdminUser != "" && !adminPasswordSet {
		problems = append(problems, "ADMIN_PASSWORD_HASH (bcrypt) or ADMIN_PASSWORD is required when ADMIN_USER is set")
	}
	if adminPasswordSet && cfg.AdminUser == "" {
		problems = append(problems, "ADMIN_USER is required when ADMIN_PASSWORD_HASH/ADMIN_PASSWORD is set")
	}
	if cfg.DatabaseURL == "" {
		problems = append(problems, "DATABASE_URL is required (e.g. postgres://user:pass@host:5432/db?sslmode=disable)")
	} else if u, err := url.Parse(cfg.DatabaseURL); err != nil || u.Host == "" || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		problems = append(problems, "DATABASE_URL must be a postgres:// connection URL")
	} else if !cfg.Dev && (u.Query().Get("sslmode") == "disable" || u.Query().Get("sslmode") == "") {
		// In production the DB link carries session tokens and TOTP ciphertext;
		// require TLS. Homelab dev may disable it (like PROXMOX_TLS_INSECURE).
		problems = append(problems, "DATABASE_URL must use TLS in production (set sslmode=require or stronger)")
	}
	if key, err := decodeSecretsKey(os.Getenv("SECRETS_KEY")); err != nil {
		problems = append(problems, "SECRETS_KEY "+err.Error())
	} else {
		cfg.SecretsKey = key
	}
	cfg.SessionIdleTTL = parseDuration("SESSION_IDLE_TTL", 12*time.Hour, &problems)
	cfg.SessionAbsoluteTTL = parseDuration("SESSION_ABSOLUTE_TTL", 720*time.Hour, &problems)
	cfg.ReconcilerInterval = parseDuration("RECONCILER_INTERVAL", 5*time.Minute, &problems)
	cfg.ReservationTTL = parseDuration("RESERVATION_TTL", 45*time.Minute, &problems)
	cfg.SchedulerEnabled = envBool("SCHEDULER_ENABLED", false)
	cfg.AutoShutdownEnabled = envBool("AUTOSHUTDOWN_ENABLED", false)
	cfg.TTLEnabled = envBool("TTL_ENABLED", false)
	cfg.SchedulerInterval = parseDuration("SCHEDULER_INTERVAL", 30*time.Second, &problems)
	cfg.AutoShutdownDefaultGrace = parseDuration("AUTOSHUTDOWN_DEFAULT_GRACE", 120*time.Second, &problems)
	cfg.InvitationTTL = parseDuration("INVITATION_TTL", 72*time.Hour, &problems)
	cfg.LoginChallengeTTL = parseDuration("LOGIN_CHALLENGE_TTL", 5*time.Minute, &problems)
	// Email: a configured SMTP host must carry a From address (else the accept
	// mail has no envelope sender). Secrets/host are never echoed into the problem.
	if cfg.SMTPHost != "" && cfg.SMTPFrom == "" {
		problems = append(problems, "SMTP_FROM is required when SMTP_HOST is set")
	}
	if cfg.InsecureCookies && !cfg.Dev {
		problems = append(problems, "PROXCLOUD_INSECURE_COOKIES must not be set in production")
	}
	if (cfg.ConsoleUser == "") != (cfg.ConsolePassword == "") {
		problems = append(problems, "PROXMOX_CONSOLE_USER and PROXMOX_CONSOLE_PASSWORD must be set together")
	}
	for _, p := range []struct {
		env string
		dst *float64
	}{
		{"PRICING_VCPU_MONTH", &cfg.PricingVCPUMonth},
		{"PRICING_RAM_GB_MONTH", &cfg.PricingRAMGBMonth},
		{"PRICING_DISK_GB_MONTH", &cfg.PricingDiskGBMonth},
	} {
		if v := os.Getenv(p.env); v != "" {
			if _, err := fmt.Sscanf(v, "%f", p.dst); err != nil {
				problems = append(problems, p.env+" must be a number")
			}
		}
	}

	if len(problems) > 0 {
		return nil, errors.New("configuration invalid:\n  - " + strings.Join(problems, "\n  - "))
	}
	return cfg, nil
}

// ConsoleEnabled reports whether the optional console credential pair is configured.
func (c *Config) ConsoleEnabled() bool { return c.ConsoleUser != "" && c.ConsolePassword != "" }

// PricingEnabled reports whether any pricing rate is configured.
func (c *Config) PricingEnabled() bool {
	return c.PricingVCPUMonth > 0 || c.PricingRAMGBMonth > 0 || c.PricingDiskGBMonth > 0
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envBool reads an optional boolean env var. An unset/empty value returns def;
// "false", "0", "no", and "off" (case-insensitive) are false, anything else is
// true. Used for defaults-true flags like SMTP_STARTTLS.
func envBool(key string, def bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	if v == "" {
		return def
	}
	switch v {
	case "false", "0", "no", "off":
		return false
	default:
		return true
	}
}

// SMTPEnabled reports whether a real SMTP driver is configured (SMTP_HOST set).
// main.go selects SMTPMailer when true, else the dev LogMailer.
func (c *Config) SMTPEnabled() bool { return c.SMTPHost != "" }

// AutoShutdownActive reports whether auto-shutdown schedules should run: the
// feature flag AND the scheduler engine it rides on must both be enabled.
func (c *Config) AutoShutdownActive() bool { return c.SchedulerEnabled && c.AutoShutdownEnabled }

// TTLActive reports whether TTL/ephemeral resources should run: the feature flag
// AND the scheduler engine it rides on must both be enabled.
func (c *Config) TTLActive() bool { return c.SchedulerEnabled && c.TTLEnabled }

// decodeSecretsKey parses SECRETS_KEY from hex (64 chars) or base64 and
// requires exactly 32 bytes (AES-256). Returns a problem description on error.
func decodeSecretsKey(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("is required (32 bytes; generate with `openssl rand -hex 32`)")
	}
	var key []byte
	if b, err := hex.DecodeString(raw); err == nil {
		key = b
	} else if b, err := base64.StdEncoding.DecodeString(raw); err == nil {
		key = b
	} else if b, err := base64.RawStdEncoding.DecodeString(raw); err == nil {
		key = b
	} else {
		return nil, errors.New("must be hex- or base64-encoded")
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("must decode to 32 bytes, got %d", len(key))
	}
	return key, nil
}

// parseDuration reads an optional duration env var, falling back to def and
// appending a problem (not aborting) on a malformed value.
func parseDuration(key string, def time.Duration, problems *[]string) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		*problems = append(*problems, key+" must be a Go duration like 12h or 30m")
		return def
	}
	return d
}
