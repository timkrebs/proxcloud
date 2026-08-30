package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/timkrebs9/proxcloud/backend/internal/config"
)

// TestBuildSnippetWriterDegradesOnBadKeyPath proves the degrade seam: a catalog
// misconfig (an unreadable SSH key path) makes buildSnippetWriter return an ERROR
// rather than panic or exit. runServe uses that error to disable catalog
// provisioning while keeping the rest of the control plane serving.
func TestBuildSnippetWriterDegradesOnBadKeyPath(t *testing.T) {
	cfg := &config.Config{
		ProxmoxNodeSSHHost:    "node.example:22",
		ProxmoxNodeSSHUser:    "snippet-writer",
		ProxmoxNodeSSHKeyPath: filepath.Join(t.TempDir(), "does-not-exist.key"),
		ProxmoxNodeKnownHosts: filepath.Join(t.TempDir(), "known_hosts"),
		SnippetStoragePath:    "/var/lib/vz/snippets",
	}
	w, err := buildSnippetWriter(cfg, discardLog())
	if err == nil {
		t.Fatal("buildSnippetWriter() = nil error for an unreadable key path, want a non-nil error (degrade, not panic)")
	}
	if w != nil {
		t.Fatalf("buildSnippetWriter() writer = %v, want nil on error", w)
	}
	if !strings.Contains(err.Error(), "ssh key") {
		t.Errorf("error = %v, want it to mention the ssh key read failure", err)
	}
}

// TestBuildSnippetWriterDegradesOnMissingVars proves a missing required SSH var is
// also a degrade (error), never a crash — mirroring the boot-time misconfig where
// CATALOG_ENABLED is on but the SSH settings are incomplete.
func TestBuildSnippetWriterDegradesOnMissingVars(t *testing.T) {
	cfg := &config.Config{
		// Host/User/KeyPath/KnownHosts deliberately empty.
		SnippetStoragePath: "/var/lib/vz/snippets",
	}
	if _, err := buildSnippetWriter(cfg, discardLog()); err == nil {
		t.Fatal("buildSnippetWriter() = nil error for missing SSH vars, want a non-nil error")
	}
}

// TestBuildSnippetWriterDegradesOnBadKnownHosts proves an unreadable/malformed
// known_hosts is a degrade error too — host-key verification stays mandatory, so a
// missing pin cannot silently fall back to an insecure connection. A real,
// parseable ed25519 key isolates the failure to known_hosts.
func TestBuildSnippetWriterDegradesOnBadKnownHosts(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	writeTestSSHKey(t, keyPath)

	cfg := &config.Config{
		ProxmoxNodeSSHHost:    "node.example:22",
		ProxmoxNodeSSHUser:    "snippet-writer",
		ProxmoxNodeSSHKeyPath: keyPath,
		ProxmoxNodeKnownHosts: filepath.Join(dir, "known_hosts-missing"),
		SnippetStoragePath:    "/var/lib/vz/snippets",
	}
	_, err := buildSnippetWriter(cfg, discardLog())
	if err == nil {
		t.Fatal("buildSnippetWriter() = nil error for a missing known_hosts, want a non-nil error")
	}
	if !strings.Contains(err.Error(), "known_hosts") {
		t.Errorf("error = %v, want it to mention known_hosts", err)
	}
}

// TestBuildSnippetWriterReadyOnGoodConfig is the positive control: with a readable
// key and a valid known_hosts, buildSnippetWriter succeeds — so the degrade tests
// above are exercising real failure modes, not an unconditional error.
func TestBuildSnippetWriterReadyOnGoodConfig(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "id_ed25519")
	writeTestSSHKey(t, keyPath)

	// A minimal, valid known_hosts line pinning the test host's key.
	hostPub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	sshHostPub, err := ssh.NewPublicKey(hostPub)
	if err != nil {
		t.Fatalf("host public key: %v", err)
	}
	knownHosts := filepath.Join(dir, "known_hosts")
	line := "node.example " + string(ssh.MarshalAuthorizedKey(sshHostPub))
	if err := os.WriteFile(knownHosts, []byte(line), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	cfg := &config.Config{
		ProxmoxNodeSSHHost:    "node.example:22",
		ProxmoxNodeSSHUser:    "snippet-writer",
		ProxmoxNodeSSHKeyPath: keyPath,
		ProxmoxNodeKnownHosts: knownHosts,
		SnippetStoragePath:    "/var/lib/vz/snippets",
	}
	w, err := buildSnippetWriter(cfg, discardLog())
	if err != nil {
		t.Fatalf("buildSnippetWriter() = %v, want nil (valid config should be ready)", err)
	}
	if w == nil {
		t.Fatal("buildSnippetWriter() writer = nil for a valid config, want a writer")
	}
}

// writeTestSSHKey generates a throwaway ed25519 private key in OpenSSH format and
// writes it to path. The key is ephemeral (per t.TempDir) and grants no access.
func writeTestSSHKey(t *testing.T, path string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
}
