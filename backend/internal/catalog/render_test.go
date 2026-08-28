package catalog

import (
	"encoding/base64"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestRenderPostgresGolden is the security-critical render assertion: the raw
// superuser password (including hostile metacharacters) NEVER appears in the
// rendered cloud-init or next-steps, the cloud-init is valid YAML, and the
// base64 blob in the snippet round-trips back to the exact secret.
func TestRenderPostgresGolden(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	pg, ok := c.Get("postgresql")
	if !ok {
		t.Fatal("postgresql missing")
	}

	// A deliberately hostile password: quotes, $(...), backticks, newline, YAML
	// metacharacters. If the pipeline is safe, none of it can break the YAML or
	// inject a command — only its base64 is interpolated.
	rawPass := "p@ss'\"\n$(reboot)`id`:# |>-"
	rawUser := "postgres"
	passB64 := base64.StdEncoding.EncodeToString([]byte(rawPass))

	ci, err := pg.RenderCloudInit(CloudInitInput{
		Hostname:         "pg-01",
		LoginUser:        "proxcloud",
		SSHKeysB64:       B64Each([]string{"ssh-ed25519 AAAAExampleKey user@host"}),
		SuperuserUserB64: B64(rawUser),
		SuperuserPassB64: passB64,
		ListenAddresses:  "*",
		Port:             5432,
	})
	if err != nil {
		t.Fatalf("render cloud-init: %v", err)
	}

	// 1) The rendered cloud-init must be valid YAML.
	var doc any
	if err := yaml.Unmarshal([]byte(ci), &doc); err != nil {
		t.Fatalf("rendered cloud-init is not valid YAML: %v\n---\n%s", err, ci)
	}
	// It must start with the cloud-config marker.
	if !strings.HasPrefix(ci, "#cloud-config") {
		t.Errorf("cloud-init must begin with #cloud-config, got:\n%s", ci[:min(40, len(ci))])
	}

	// 2) The RAW password must NOT appear anywhere in the rendered snippet — only
	// its base64 blob. Test distinctive raw fragments that would signal a leak.
	for _, frag := range []string{"$(reboot)", "`id`", rawPass} {
		if strings.Contains(ci, frag) {
			t.Errorf("raw credential fragment %q leaked into cloud-init:\n%s", frag, ci)
		}
	}
	// The base64 blob MUST be present (so the guest can decode it in-runcmd).
	if !strings.Contains(ci, passB64) {
		t.Errorf("base64 password blob missing from cloud-init")
	}
	// 3) The base64 blob round-trips to the exact secret.
	if got, _ := base64.StdEncoding.DecodeString(passB64); string(got) != rawPass {
		t.Errorf("base64 round-trip mismatch")
	}
	// The qemu-guest-agent package is required for the configuring step.
	if !strings.Contains(ci, "qemu-guest-agent") {
		t.Error("cloud-init must install qemu-guest-agent (configuring step depends on it)")
	}

	// 4) next-steps carries host/port but has NO password field structurally, so a
	// secret cannot leak. Render with the raw password NOWHERE in the input.
	ns, err := pg.RenderNextSteps(NextStepsInput{
		Host: "10.0.0.5", Port: 5432, Username: rawUser, ServiceName: "PostgreSQL",
	})
	if err != nil {
		t.Fatalf("render next-steps: %v", err)
	}
	if !strings.Contains(ns, "10.0.0.5") || !strings.Contains(ns, "5432") {
		t.Errorf("next-steps missing host/port:\n%s", ns)
	}
	if strings.Contains(ns, rawPass) || strings.Contains(ns, passB64) {
		t.Errorf("next-steps leaked a credential value:\n%s", ns)
	}
}

// TestRenderPostgresHostileSSHKey is the SSH-key analogue of the password fuzz
// test: a crafted "public key" containing a newline, a top-level runcmd:, quotes,
// and $(...) must NOT be able to inject YAML structure or a root shell command.
// Only its base64 blob is interpolated; the raw bytes are decoded in-guest.
func TestRenderPostgresHostileSSHKey(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	pg, ok := c.Get("postgresql")
	if !ok {
		t.Fatal("postgresql missing")
	}

	// A newline lets a raw key open a whole new top-level YAML key; $(...) and
	// backticks would run as root if the value ever reached a shell unquoted.
	hostileKey := "ssh-ed25519 AAAAlegit user@host\nruncmd:\n  - touch /tmp/pwned\n\"'$(reboot)`id`:# |>-"
	keyB64 := B64(hostileKey)

	ci, err := pg.RenderCloudInit(CloudInitInput{
		Hostname:         "pg-01",
		LoginUser:        "proxcloud",
		SSHKeysB64:       B64Each([]string{hostileKey}),
		SuperuserUserB64: B64("postgres"),
		SuperuserPassB64: B64("pw"),
		ListenAddresses:  "*",
		Port:             5432,
	})
	if err != nil {
		t.Fatalf("render cloud-init: %v", err)
	}

	// 1) Still valid YAML despite the hostile key.
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(ci), &doc); err != nil {
		t.Fatalf("rendered cloud-init is not valid YAML: %v\n---\n%s", err, ci)
	}
	// 2) The injection payload must NOT appear un-decoded anywhere in the snippet.
	for _, frag := range []string{"touch /tmp/pwned", "$(reboot)", "`id`", hostileKey} {
		if strings.Contains(ci, frag) {
			t.Errorf("hostile SSH-key fragment %q leaked un-decoded into cloud-init:\n%s", frag, ci)
		}
	}
	// 3) The base64 blob (the ONLY thing that should carry the key) is present.
	if !strings.Contains(ci, keyB64) {
		t.Errorf("base64 SSH-key blob missing from cloud-init")
	}
	// 4) The document has exactly the expected top-level keys — a YAML injection
	// would have added one (e.g. a second runcmd).
	wantKeys := map[string]bool{
		"hostname": true, "manage_etc_hosts": true, "users": true,
		"package_update": true, "packages": true, "runcmd": true,
	}
	for k := range doc {
		if !wantKeys[k] {
			t.Errorf("unexpected top-level YAML key %q — possible injection:\n%s", k, ci)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
