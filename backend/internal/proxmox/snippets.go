package proxmox

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SnippetWriter places rendered cloud-init snippets on a Proxmox node's snippet
// datastore over SSH/SFTP — the ONE Proxcloud capability that reaches the node
// outside the API token (ADR-0025). PVE has no REST endpoint to upload a snippet
// body (docs/proxmox/cloud-init.md §1.3), so this SFTP path is required to use
// `cicustom`.
//
// This is a strictly larger trust surface than the API token, so it ships with
// hard guardrails:
//   - Host-key verification is MANDATORY (configured known_hosts). There is no
//     insecure escape hatch — ssh.InsecureIgnoreHostKey is never used.
//   - Writes are confined to the configured snippet path; filenames are
//     validated to a `proxcloud-*.yaml` allowlist with no separators or `..`.
//   - The whole thing loads only when CATALOG_ENABLED is on (main.go).
type SnippetWriter struct {
	host        string // host:port
	user        string
	signer      ssh.Signer
	hostKeyCB   ssh.HostKeyCallback
	storagePath string // node filesystem dir, e.g. /var/lib/vz/snippets
	log         *slog.Logger
}

// SnippetConfig is the node-SSH + datastore configuration for the writer.
type SnippetConfig struct {
	Host        string // node SSH host, optionally host:port (default port 22)
	User        string // dedicated least-privilege snippet-writer account
	KeyPath     string // path to the SSH private key
	KnownHosts  string // path to the known_hosts file pinning the node's key
	StoragePath string // node filesystem path of the snippet datastore
	Log         *slog.Logger
}

const (
	snippetDialTimeout = 15 * time.Second
	snippetOpTimeout   = 30 * time.Second
)

// snippetNameRe confines snippet filenames to a `proxcloud-`-prefixed,
// [a-z0-9-] allowlist ending in .yaml — no separators, no dot segments, so a
// request string can never traverse out of the datastore directory (ADR-0025).
var snippetNameRe = regexp.MustCompile(`^proxcloud-[a-z0-9-]+\.yaml$`)

// SafeSnippetName validates a filename for path confinement. Exported so the
// engine/handler build names the writer will accept and tests can assert the
// traversal rejections directly.
func SafeSnippetName(name string) error {
	if name == "" {
		return fmt.Errorf("snippet filename is empty")
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("snippet filename %q must not contain a path separator", name)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("snippet filename %q must not contain '..'", name)
	}
	if !snippetNameRe.MatchString(name) {
		return fmt.Errorf("snippet filename %q must match proxcloud-<name>.yaml ([a-z0-9-])", name)
	}
	return nil
}

// NewSnippetWriter builds a writer, loading the SSH key and pinning the node
// host key from known_hosts. It fails fast if any of that is missing or
// malformed — a misconfigured snippet writer must never fall back to an insecure
// connection.
func NewSnippetWriter(cfg SnippetConfig) (*SnippetWriter, error) {
	if cfg.Host == "" || cfg.User == "" || cfg.KeyPath == "" || cfg.KnownHosts == "" || cfg.StoragePath == "" {
		return nil, fmt.Errorf("snippet writer: host, user, key path, known_hosts, and storage path are all required")
	}
	keyPEM, err := os.ReadFile(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("snippet writer: read ssh key: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(keyPEM)
	if err != nil {
		return nil, fmt.Errorf("snippet writer: parse ssh key: %w", err)
	}
	// knownhosts.New pins the node's host key; a mismatch or unknown host is a
	// hard error at connect time. This REPLACES any insecure callback.
	hostKeyCB, err := knownhosts.New(cfg.KnownHosts)
	if err != nil {
		return nil, fmt.Errorf("snippet writer: load known_hosts: %w", err)
	}

	host := cfg.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "22")
	}

	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	return &SnippetWriter{
		host:        host,
		user:        cfg.User,
		signer:      signer,
		hostKeyCB:   hostKeyCB,
		storagePath: path.Clean(cfg.StoragePath),
		log:         log,
	}, nil
}

// remotePath validates the filename and joins it under the confined storage
// path, re-checking that the result did not escape the directory.
func (w *SnippetWriter) remotePath(filename string) (string, error) {
	if err := SafeSnippetName(filename); err != nil {
		return "", err
	}
	full := path.Join(w.storagePath, filename)
	if path.Dir(full) != w.storagePath {
		return "", fmt.Errorf("snippet path %q escapes the datastore directory %q", full, w.storagePath)
	}
	return full, nil
}

// WriteSnippet uploads content to <storagePath>/<filename> over SFTP. The file
// is written before the guest's first boot so cloud-init can read it. A failure
// (SSH unreachable / host-key mismatch / datastore full) is returned verbatim so
// the deploy engine surfaces it honestly rather than silently proceeding.
func (w *SnippetWriter) WriteSnippet(ctx context.Context, filename, content string) error {
	full, err := w.remotePath(filename)
	if err != nil {
		return err
	}
	client, closeFn, err := w.dial(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	// Write to a temp file then rename, so a reader never sees a half-written
	// snippet (cloud-init reads the whole file at boot).
	tmp := full + ".tmp"
	f, err := client.Create(tmp)
	if err != nil {
		return fmt.Errorf("snippet writer: create %q: %w", tmp, err)
	}
	if _, err := f.Write([]byte(content)); err != nil {
		_ = f.Close()
		_ = client.Remove(tmp)
		return fmt.Errorf("snippet writer: write %q: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = client.Remove(tmp)
		return fmt.Errorf("snippet writer: close %q: %w", tmp, err)
	}
	if err := client.PosixRename(tmp, full); err != nil {
		// Fall back to a plain rename if the server lacks the posix-rename ext.
		if err2 := client.Rename(tmp, full); err2 != nil {
			_ = client.Remove(tmp)
			return fmt.Errorf("snippet writer: rename %q -> %q: %w", tmp, full, err)
		}
	}
	w.log.Info("wrote cloud-init snippet", "path", full, "bytes", len(content))
	return nil
}

// RemoveSnippet deletes <storagePath>/<filename>. A missing file is not an
// error (idempotent teardown).
func (w *SnippetWriter) RemoveSnippet(ctx context.Context, filename string) error {
	full, err := w.remotePath(filename)
	if err != nil {
		return err
	}
	client, closeFn, err := w.dial(ctx)
	if err != nil {
		return err
	}
	defer closeFn()

	if err := client.Remove(full); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("snippet writer: remove %q: %w", full, err)
	}
	w.log.Info("removed cloud-init snippet", "path", full)
	return nil
}

// dial opens an SSH connection (host-key verified) and an SFTP session, honoring
// ctx for the TCP dial. closeFn tears both down.
func (w *SnippetWriter) dial(ctx context.Context) (*sftp.Client, func(), error) {
	dctx, cancel := context.WithTimeout(ctx, snippetOpTimeout)
	// cancel is released by closeFn / the error paths below.

	d := net.Dialer{Timeout: snippetDialTimeout}
	conn, err := d.DialContext(dctx, "tcp", w.host)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("snippet writer: dial %s: %w", w.host, err)
	}
	cfg := &ssh.ClientConfig{
		User:            w.user,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(w.signer)},
		HostKeyCallback: w.hostKeyCB, // MANDATORY — never InsecureIgnoreHostKey
		Timeout:         snippetDialTimeout,
	}
	if dl, ok := dctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(conn, w.host, cfg)
	if err != nil {
		_ = conn.Close()
		cancel()
		return nil, nil, fmt.Errorf("snippet writer: ssh handshake with %s: %w", w.host, err)
	}
	// Clear the dial deadline so the long-lived session is not torn down mid-op.
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(sshConn, chans, reqs)

	sc, err := sftp.NewClient(client)
	if err != nil {
		_ = client.Close()
		cancel()
		return nil, nil, fmt.Errorf("snippet writer: open sftp with %s: %w", w.host, err)
	}
	// Watchdog: once the dial deadline is cleared (above), nothing else bounds a
	// post-handshake stall — a hung Create/Write/Close/rename would otherwise
	// block past the op timeout. Closing the underlying conn when dctx is done
	// (op timeout OR parent-ctx cancel) unblocks any in-flight SFTP call so the
	// write honors the context (finding: SFTP write timeout not enforced).
	stopWatch := closeOnDone(dctx, conn)
	closeFn := func() {
		stopWatch()
		_ = sc.Close()
		_ = client.Close()
		cancel()
	}
	return sc, closeFn, nil
}

// closeOnDone closes c when ctx is done, unless stop is called first. It is the
// watchdog that makes a post-handshake SFTP op honor its context: the returned
// stop must be called on the normal completion path so a finished op does not get
// its conn closed out from under the next caller.
func closeOnDone(ctx context.Context, c io.Closer) (stop func()) {
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = c.Close()
		case <-done:
		}
	}()
	return func() { close(done) }
}
