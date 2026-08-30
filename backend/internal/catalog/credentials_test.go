package catalog

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestResolveAndRenderUserSuppliedHostilePassword is the Phase-C security-critical
// regression: a USER-SUPPLIED password packed with shell/YAML metacharacters
// (`" ' $ \ \n # : |`, $(reboot), backticks) must (a) PASS the length-only policy
// because it is ≥ 12 chars — metacharacters are legal — and (b) render, through
// the SAME base64 transport as a generated value, into valid YAML where the value
// appears ONLY base64-encoded, never as raw shell/YAML text, round-tripping
// exactly. Any future edit that reintroduces raw interpolation fails here.
func TestResolveAndRenderUserSuppliedHostilePassword(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	pg, ok := c.Get("postgresql")
	if !ok {
		t.Fatal("postgresql missing")
	}

	// 25 runes, deliberately hostile: quotes, $(...), backticks, a newline, and
	// YAML metacharacters. Length-only policy admits every one of these bytes.
	rawPass := "aB3$(reboot)`id`\"'\n#:|\\>-"

	resolved, err := ResolveCredentials(pg.Credentials, []SuppliedCredential{
		{Name: "superuser", Password: rawPass},
	}, GeneratePassword)
	if err != nil {
		t.Fatalf("resolve user-supplied hostile password (>=12): unexpected error %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("resolved = %d credentials, want 1", len(resolved))
	}
	rc := resolved[0]
	if !rc.UserSupplied {
		t.Error("credential must be marked user-supplied (drives audit boolean + no reveal)")
	}
	if rc.Username != "postgres" {
		t.Errorf("username = %q, want fixed 'postgres'", rc.Username)
	}
	// Length-only policy must NOT mangle the value (no trimming/escaping at rest).
	if rc.Password != rawPass {
		t.Errorf("resolved password was altered; length-only policy must pass the raw value through unchanged")
	}

	// Inject through the EXACT safe path: base64 the resolved value, render.
	passB64 := base64.StdEncoding.EncodeToString([]byte(rc.Password))
	ci, err := pg.RenderCloudInit(CloudInitInput{
		Hostname:         "pg-01",
		LoginUser:        "proxcloud",
		SSHKeysB64:       B64Each([]string{"ssh-ed25519 AAAAExampleKey user@host"}),
		SuperuserUserB64: B64(rc.Username),
		SuperuserPassB64: B64(rc.Password),
		ListenAddresses:  "*",
		Port:             5432,
	})
	if err != nil {
		t.Fatalf("render cloud-init: %v", err)
	}

	// (a) Valid YAML with exactly the expected top-level keys — an injection would
	// have opened a new key (a second runcmd / users entry).
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(ci), &doc); err != nil {
		t.Fatalf("rendered cloud-init is not valid YAML: %v\n---\n%s", err, ci)
	}
	wantKeys := map[string]bool{
		"hostname": true, "manage_etc_hosts": true, "users": true,
		"package_update": true, "packages": true, "runcmd": true,
	}
	for k := range doc {
		if !wantKeys[k] {
			t.Errorf("unexpected top-level YAML key %q — possible injection:\n%s", k, ci)
		}
	}

	// (b) The RAW value (and its distinctive fragments) must NOT appear anywhere.
	for _, frag := range []string{"$(reboot)", "`id`", rawPass} {
		if strings.Contains(ci, frag) {
			t.Errorf("raw user-supplied fragment %q leaked into cloud-init:\n%s", frag, ci)
		}
	}
	// (c) The base64 blob IS present and round-trips to the exact secret.
	if !strings.Contains(ci, passB64) {
		t.Errorf("base64 password blob missing from cloud-init")
	}
	if got, _ := base64.StdEncoding.DecodeString(passB64); string(got) != rawPass {
		t.Errorf("base64 round-trip mismatch: user-supplied password did not survive transport")
	}
}

// TestResolveCredentialsPolicy locks the server-authoritative validation rules for
// the postgresql credential (one settable superuser, fixed username `postgres`).
func TestResolveCredentialsPolicy(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	pg, ok := c.Get("postgresql")
	if !ok {
		t.Fatal("postgresql missing")
	}

	tests := []struct {
		name         string
		supplied     []SuppliedCredential
		wantErr      bool
		wantContains string // substring of the (value-free) error message
		wantUserSet  bool   // when !wantErr: expected UserSupplied on resolved[0]
	}{
		{
			name:        "none supplied -> generated",
			supplied:    nil,
			wantErr:     false,
			wantUserSet: false,
		},
		{
			name:        "valid 14-char password -> user-supplied",
			supplied:    []SuppliedCredential{{Name: "superuser", Password: "correcthorse12"}},
			wantErr:     false,
			wantUserSet: true,
		},
		{
			name:        "exactly 12 chars -> accepted",
			supplied:    []SuppliedCredential{{Name: "superuser", Password: "abcdefghijkl"}},
			wantErr:     false,
			wantUserSet: true,
		},
		{
			name:         "11 chars -> rejected (length-only policy)",
			supplied:     []SuppliedCredential{{Name: "superuser", Password: "elevenchars"}},
			wantErr:      true,
			wantContains: "at least 12 characters",
		},
		{
			name:         "empty password on present entry -> rejected",
			supplied:     []SuppliedCredential{{Name: "superuser", Password: ""}},
			wantErr:      true,
			wantContains: "at least 12 characters",
		},
		{
			name:         "supplied username on fixed-username credential -> rejected",
			supplied:     []SuppliedCredential{{Name: "superuser", Username: "evil", Password: "correcthorse12"}},
			wantErr:      true,
			wantContains: "fixed",
		},
		{
			name:         "unknown credential name -> rejected",
			supplied:     []SuppliedCredential{{Name: "nope", Password: "correcthorse12"}},
			wantErr:      true,
			wantContains: "unknown credential",
		},
		{
			name:         "same credential twice -> rejected",
			supplied:     []SuppliedCredential{{Name: "superuser", Password: "correcthorse12"}, {Name: "superuser", Password: "correcthorse12"}},
			wantErr:      true,
			wantContains: "more than once",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveCredentials(pg.Credentials, tt.supplied, GeneratePassword)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (resolved %+v)", got)
				}
				var ce *CredentialError
				if !errors.As(err, &ce) {
					t.Fatalf("error = %T, want *CredentialError (maps to 400): %v", err, err)
				}
				if tt.wantContains != "" && !strings.Contains(ce.Msg, tt.wantContains) {
					t.Errorf("error %q does not contain %q", ce.Msg, tt.wantContains)
				}
				// A validation error must never leak a supplied value.
				for _, sc := range tt.supplied {
					if sc.Password != "" && strings.Contains(ce.Msg, sc.Password) {
						t.Errorf("error message leaked the supplied password")
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got[0].UserSupplied != tt.wantUserSet {
				t.Errorf("UserSupplied = %v, want %v", got[0].UserSupplied, tt.wantUserSet)
			}
			if got[0].Username != "postgres" {
				t.Errorf("username = %q, want 'postgres'", got[0].Username)
			}
			if got[0].Password == "" {
				t.Error("resolved password must never be empty")
			}
		})
	}
}

// TestResolveCredentialsUsernameSettable covers a hypothetical service credential
// whose username IS settable, exercising the charset guard and the fixed rules
// that postgresql (username fixed) cannot reach.
func TestResolveCredentialsUsernameSettable(t *testing.T) {
	settable := []CredentialSpec{{
		Name: "admin", Username: "admin",
		UsernameSettable: true, UserSettable: true, GeneratedDefault: true,
	}}
	noGen := []CredentialSpec{{
		Name: "admin", Username: "admin",
		UsernameSettable: true, UserSettable: true, GeneratedDefault: false,
	}}
	notSettable := []CredentialSpec{{
		Name: "admin", Username: "admin",
		UsernameSettable: false, UserSettable: false, GeneratedDefault: true,
	}}

	t.Run("valid settable username + password", func(t *testing.T) {
		got, err := ResolveCredentials(settable, []SuppliedCredential{
			{Name: "admin", Username: "app_user", Password: "correcthorse12"},
		}, GeneratePassword)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got[0].Username != "app_user" || !got[0].UserSupplied {
			t.Fatalf("resolved = %+v, want username app_user, user-supplied", got[0])
		}
	})

	t.Run("invalid username charset -> rejected", func(t *testing.T) {
		_, err := ResolveCredentials(settable, []SuppliedCredential{
			{Name: "admin", Username: "1bad-name!", Password: "correcthorse12"},
		}, GeneratePassword)
		var ce *CredentialError
		if !errors.As(err, &ce) || !strings.Contains(ce.Msg, "letters, digits") {
			t.Fatalf("error = %v, want charset *CredentialError", err)
		}
	})

	t.Run("present entry requires a password even with a username", func(t *testing.T) {
		_, err := ResolveCredentials(settable, []SuppliedCredential{
			{Name: "admin", Username: "app_user"},
		}, GeneratePassword)
		var ce *CredentialError
		if !errors.As(err, &ce) || !strings.Contains(ce.Msg, "at least 12") {
			t.Fatalf("error = %v, want password-required *CredentialError", err)
		}
	})

	t.Run("no generated default and none supplied -> rejected", func(t *testing.T) {
		_, err := ResolveCredentials(noGen, nil, GeneratePassword)
		var ce *CredentialError
		if !errors.As(err, &ce) || !strings.Contains(ce.Msg, "must be supplied") {
			t.Fatalf("error = %v, want no-generated-default *CredentialError", err)
		}
	})

	t.Run("supplied entry on a non-user-settable credential -> rejected", func(t *testing.T) {
		_, err := ResolveCredentials(notSettable, []SuppliedCredential{
			{Name: "admin", Password: "correcthorse12"},
		}, GeneratePassword)
		var ce *CredentialError
		if !errors.As(err, &ce) || !strings.Contains(ce.Msg, "cannot be user-supplied") {
			t.Fatalf("error = %v, want not-user-settable *CredentialError", err)
		}
	})
}
