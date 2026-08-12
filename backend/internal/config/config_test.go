package config

import (
	"strings"
	"testing"
)

// baseEnv is a minimal set of env vars that make Load() succeed, so each test
// can mutate one thing and assert its effect.
func baseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PROXMOX_URL", "https://pve01:8006")
	t.Setenv("PROXMOX_TOKEN_ID", "u@pam!t")
	t.Setenv("PROXMOX_TOKEN_SECRET", "secret")
	t.Setenv("SESSION_SECRET", strings.Repeat("s", 32))
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD_HASH", "$2a$10$abcdefghijklmnopqrstuv")
	t.Setenv("SECRETS_KEY", strings.Repeat("ab", 32)) // 64 hex chars = 32 bytes
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("PROXCLOUD_ENV", "development")
	// Clear optionals that could leak in from the ambient environment.
	t.Setenv("PROXCLOUD_INSECURE_COOKIES", "")
}

func TestLoadValidBaseline(t *testing.T) {
	baseEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	if len(cfg.SecretsKey) != 32 {
		t.Errorf("SecretsKey len = %d, want 32", len(cfg.SecretsKey))
	}
	if cfg.SessionIdleTTL <= 0 || cfg.SessionAbsoluteTTL <= 0 {
		t.Errorf("session TTLs not defaulted: idle=%v abs=%v", cfg.SessionIdleTTL, cfg.SessionAbsoluteTTL)
	}
}

func TestSecretsKeyValidation(t *testing.T) {
	tests := []struct {
		name string
		val  string
		want string // substring expected in the error, "" = must succeed
	}{
		{"valid hex", strings.Repeat("ab", 32), ""},
		{"valid base64", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=", ""}, // 32 bytes
		{"missing", "", "SECRETS_KEY"},
		{"too short", strings.Repeat("ab", 16), "SECRETS_KEY"},
		{"garbage", "not-hex-or-base64-!!!", "SECRETS_KEY"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseEnv(t)
			t.Setenv("SECRETS_KEY", tt.val)
			_, err := Load()
			assertProblem(t, err, tt.want)
		})
	}
}

func TestDatabaseURLValidation(t *testing.T) {
	tests := []struct {
		name string
		env  string
		url  string
		want string
	}{
		{"required and unset in prod", "production", "", "DATABASE_URL is required"},
		{"non-postgres scheme", "development", "http://localhost/db", "postgres://"},
		{"sslmode disable rejected in prod", "production", "postgres://u:p@db:5432/x?sslmode=disable", "must use TLS in production"},
		{"sslmode require ok in prod", "production", "postgres://u:p@db:5432/x?sslmode=require", ""},
		{"disable ok in dev", "development", "postgres://u:p@localhost:5432/x?sslmode=disable", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseEnv(t)
			t.Setenv("PROXCLOUD_ENV", tt.env)
			t.Setenv("DATABASE_URL", tt.url)
			if tt.env == "production" {
				// production also forbids insecure cookies; keep it unset.
				t.Setenv("PROXCLOUD_INSECURE_COOKIES", "")
			}
			_, err := Load()
			assertProblem(t, err, tt.want)
		})
	}
}

func TestDatabaseURLDevDefault(t *testing.T) {
	baseEnv(t)
	t.Setenv("PROXCLOUD_ENV", "development")
	t.Setenv("DATABASE_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("dev with no DATABASE_URL should default, got %v", err)
	}
	if !strings.HasPrefix(cfg.DatabaseURL, "postgres://") {
		t.Errorf("dev default DATABASE_URL = %q", cfg.DatabaseURL)
	}
}

func TestSessionTTLParsing(t *testing.T) {
	baseEnv(t)
	t.Setenv("SESSION_IDLE_TTL", "not-a-duration")
	_, err := Load()
	assertProblem(t, err, "SESSION_IDLE_TTL")
}

// assertProblem checks that err is nil when want=="" or contains want otherwise.
func assertProblem(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("Load() = %v, want nil", err)
		}
		return
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Load() error = %v, want to contain %q", err, want)
	}
}
