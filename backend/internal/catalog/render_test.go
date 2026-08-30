package catalog

import (
	"encoding/base64"
	"strconv"
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

// assertHostilePasswordRendersSafely is the shared Phase-D render assertion for a
// user/pass service (mongodb, redis): a deliberately hostile password (quotes,
// $(...), backticks, a newline, YAML metacharacters) must render — through the
// SAME base64 transport as PostgreSQL — into valid YAML that installs
// qemu-guest-agent, never contains the raw secret, and round-trips the base64
// blob back to the exact bytes. useUser is false for password-only services
// (Redis `requirepass` has no username).
func assertHostilePasswordRendersSafely(t *testing.T, id string, port int, useUser bool) {
	t.Helper()
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	svc, ok := c.Get(id)
	if !ok {
		t.Fatalf("%s missing", id)
	}

	// Hostile password: quotes, $(...), backticks, newline, YAML metacharacters.
	// If the base64 pipeline holds, none of it can break the YAML or inject a
	// command — only its base64 is interpolated.
	rawPass := "p@ss'\"\n$(reboot)`id`:# |>-"
	passB64 := base64.StdEncoding.EncodeToString([]byte(rawPass))

	in := CloudInitInput{
		Hostname:         id + "-01",
		LoginUser:        "proxcloud",
		SSHKeysB64:       B64Each([]string{"ssh-ed25519 AAAAExampleKey user@host"}),
		SuperuserPassB64: passB64,
		ListenAddresses:  "*",
		Port:             port,
	}
	if useUser {
		in.SuperuserUserB64 = B64("admin")
	}
	ci, err := svc.RenderCloudInit(in)
	if err != nil {
		t.Fatalf("render cloud-init: %v", err)
	}

	// 1) Valid YAML beginning with the cloud-config marker.
	var doc any
	if err := yaml.Unmarshal([]byte(ci), &doc); err != nil {
		t.Fatalf("rendered cloud-init is not valid YAML: %v\n---\n%s", err, ci)
	}
	if !strings.HasPrefix(ci, "#cloud-config") {
		t.Errorf("cloud-init must begin with #cloud-config, got:\n%s", ci[:min(40, len(ci))])
	}

	// 2) The RAW password (and distinctive fragments) must NOT appear anywhere.
	for _, frag := range []string{"$(reboot)", "`id`", rawPass} {
		if strings.Contains(ci, frag) {
			t.Errorf("raw credential fragment %q leaked into %s cloud-init:\n%s", frag, id, ci)
		}
	}
	// 3) The base64 blob MUST be present and round-trip to the exact secret.
	if !strings.Contains(ci, passB64) {
		t.Errorf("base64 password blob missing from %s cloud-init", id)
	}
	if got, _ := base64.StdEncoding.DecodeString(passB64); string(got) != rawPass {
		t.Errorf("base64 round-trip mismatch for %s", id)
	}
	// 4) qemu-guest-agent is required for the configuring step.
	if !strings.Contains(ci, "qemu-guest-agent") {
		t.Errorf("%s cloud-init must install qemu-guest-agent (configuring step depends on it)", id)
	}

	// 5) next-steps carries host/port but has NO password field structurally.
	ns, err := svc.RenderNextSteps(NextStepsInput{
		Host: "10.0.0.5", Port: port, Username: "admin", ServiceName: id,
	})
	if err != nil {
		t.Fatalf("render next-steps: %v", err)
	}
	if !strings.Contains(ns, "10.0.0.5") || !strings.Contains(ns, strconv.Itoa(port)) {
		t.Errorf("%s next-steps missing host/port:\n%s", id, ns)
	}
	if strings.Contains(ns, rawPass) || strings.Contains(ns, passB64) {
		t.Errorf("%s next-steps leaked a credential value:\n%s", id, ns)
	}
}

// TestRenderMongoDBGolden is the MongoDB analogue of the PostgreSQL golden: a
// hostile root password renders base64-only, into valid YAML that installs
// qemu-guest-agent, with no raw leak.
func TestRenderMongoDBGolden(t *testing.T) {
	assertHostilePasswordRendersSafely(t, "mongodb", 27017, true)
}

// TestRenderRedisGolden is the Redis analogue: a hostile `requirepass` value
// renders base64-only (no username — Redis requirepass has none), into valid YAML
// that installs qemu-guest-agent, with no raw leak.
func TestRenderRedisGolden(t *testing.T) {
	assertHostilePasswordRendersSafely(t, "redis", 6379, false)
}

// TestRenderVaultGolden asserts the Vault honesty contract (ADR-0027 §4): the
// service declares an EMPTY credential schema, its cloud-init installs Vault +
// qemu-guest-agent and carries the TLS-off warning, and its next-steps hands the
// operator the init/unseal instructions with NO secret material — Proxcloud never
// generates, injects, or reveals a Vault secret.
func TestRenderVaultGolden(t *testing.T) {
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	vault, ok := c.Get("vault")
	if !ok {
		t.Fatal("vault missing")
	}

	// The credential schema MUST be empty — there is nothing for Proxcloud to
	// generate, inject, or reveal.
	if len(vault.Credentials) != 0 {
		t.Fatalf("vault must declare an EMPTY credential schema (ADR-0027 §4), got %+v", vault.Credentials)
	}

	// Render cloud-init with NO injected credential (empty user/pass blobs); the
	// template must not reference them.
	ci, err := vault.RenderCloudInit(CloudInitInput{
		Hostname:        "vault-01",
		LoginUser:       "proxcloud",
		SSHKeysB64:      B64Each([]string{"ssh-ed25519 AAAAExampleKey user@host"}),
		ListenAddresses: "*",
		Port:            8200,
	})
	if err != nil {
		t.Fatalf("render cloud-init: %v", err)
	}
	// 1) Valid YAML with the cloud-config marker.
	var doc any
	if err := yaml.Unmarshal([]byte(ci), &doc); err != nil {
		t.Fatalf("rendered cloud-init is not valid YAML: %v\n---\n%s", err, ci)
	}
	if !strings.HasPrefix(ci, "#cloud-config") {
		t.Errorf("cloud-init must begin with #cloud-config")
	}
	// 2) Installs qemu-guest-agent and Vault, in server mode, WITHOUT init/unseal.
	if !strings.Contains(ci, "qemu-guest-agent") {
		t.Error("vault cloud-init must install qemu-guest-agent")
	}
	if !strings.Contains(ci, "install -y vault") {
		t.Error("vault cloud-init must install Vault")
	}
	if strings.Contains(ci, "operator init") || strings.Contains(ci, "operator unseal") {
		t.Error("vault cloud-init must NOT initialise or unseal Vault (ADR-0027 §4) — that is the operator's step")
	}
	// 3) The TLS-off warning must be present and TLS disabled in the listener.
	if !strings.Contains(ci, "tls_disable = 1") {
		t.Error("vault cloud-init must configure the listener with tls_disable = 1")
	}
	if !strings.Contains(ci, "TLS IS DISABLED") {
		t.Error("vault cloud-init must carry a loud TLS-off warning")
	}

	// 4) next-steps: the plain-HTTP address, a prominent TLS-off warning, and the
	// operator-run init + unseal instructions. It has no password field by
	// construction, so no secret can leak.
	ns, err := vault.RenderNextSteps(NextStepsInput{
		Host: "10.0.0.9", Port: 8200, Username: "", ServiceName: "Vault CE",
	})
	if err != nil {
		t.Fatalf("render next-steps: %v", err)
	}
	for _, want := range []string{"http://10.0.0.9:8200", "vault operator init", "vault operator unseal", "TLS is DISABLED"} {
		if !strings.Contains(ns, want) {
			t.Errorf("vault next-steps missing %q:\n%s", want, ns)
		}
	}
	// The tile description itself must carry the TLS-off warning.
	if !strings.Contains(vault.Description, "TLS is DISABLED") {
		t.Errorf("vault description must carry the TLS-off warning, got %q", vault.Description)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
