// Package config loads all Proxcloud configuration from environment
// variables (optionally seeded from a .env file for native development).
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
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
	SessionSecret     []byte
	AdminUser         string
	AdminPasswordHash string // bcrypt; if empty, AdminPassword is hashed at boot (dev convenience)
	AdminPassword     string

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
	if s := os.Getenv("SESSION_SECRET"); len(s) >= 32 {
		cfg.SessionSecret = []byte(s)
	} else {
		problems = append(problems, "SESSION_SECRET is required (32+ random characters)")
	}
	if cfg.AdminUser == "" {
		problems = append(problems, "ADMIN_USER is required")
	}
	if cfg.AdminPasswordHash == "" && cfg.AdminPassword == "" {
		problems = append(problems, "ADMIN_PASSWORD_HASH (bcrypt) or ADMIN_PASSWORD is required")
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
