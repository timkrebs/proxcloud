package catalog

import (
	"bytes"
	"encoding/base64"
	"fmt"
)

// CloudInitInput is the server-authoritative input to a service's cloud-init
// template. Every value here is trusted or already base64-encoded by the caller:
// the renderer performs NO encoding of its own and NEVER receives a raw
// credential — SuperuserUserB64/SuperuserPassB64 are base64 blobs, decoded only
// in-guest (docs/proxmox/cloud-init.md §4). This keeps the raw secret out of the
// template, the rendered YAML, and any log line built from it.
type CloudInitInput struct {
	Hostname  string
	LoginUser string
	// SSHKeysB64 is the base64 of each raw OpenSSH public key. Keys are NOT
	// interpolated raw: a crafted key with a newline could otherwise inject a
	// top-level runcmd: run as root. Only the base64 blob (no YAML/shell
	// metacharacters) reaches the snippet; the template decodes it in-guest into
	// the login user's authorized_keys (docs/proxmox/cloud-init.md §4).
	SSHKeysB64       []string
	SuperuserUserB64 string // base64 of the DB superuser/role name
	SuperuserPassB64 string // base64 of the DB superuser password
	ListenAddresses  string // e.g. "*"
	Port             int
}

// NextStepsInput is the input to a service's next-steps template. It has NO
// password field by construction, so a credential value cannot leak into the
// post-ready guidance the user sees — the "no secret in next-steps" iron rule is
// structural, not a review check.
type NextStepsInput struct {
	Host        string
	Port        int
	Username    string // the non-secret account/role name
	ServiceName string
}

// B64 base64-encodes a raw untrusted value for transport into a snippet. The
// caller (the provision handler) uses it to prepare CloudInitInput; the raw
// value never reaches the template. Exposed so the handler and tests share one
// encoder.
func B64(raw string) string {
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

// B64Each base64-encodes each raw value (e.g. SSH public keys) for transport into
// a snippet, so a hostile value can never break the YAML or inject a shell
// command — only its base64 blob is interpolated, decoded in-guest (docs §4).
func B64Each(raw []string) []string {
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i] = B64(v)
	}
	return out
}

// RenderCloudInit renders the service's #cloud-config user-data. The result is
// the exact bytes uploaded as the snippet and referenced by cicustom.
func (s *ServiceDef) RenderCloudInit(in CloudInitInput) (string, error) {
	if s.cloudInit == nil {
		return "", fmt.Errorf("service %q: cloud-init template not loaded", s.ID)
	}
	var buf bytes.Buffer
	if err := s.cloudInit.Execute(&buf, in); err != nil {
		return "", fmt.Errorf("service %q: render cloud-init: %w", s.ID, err)
	}
	return buf.String(), nil
}

// RenderNextSteps renders the post-ready guidance shown on the deployment
// success view. It is fed only non-secret coordinates (host/port/username).
func (s *ServiceDef) RenderNextSteps(in NextStepsInput) (string, error) {
	if s.nextSteps == nil {
		return "", fmt.Errorf("service %q: next-steps template not loaded", s.ID)
	}
	var buf bytes.Buffer
	if err := s.nextSteps.Execute(&buf, in); err != nil {
		return "", fmt.Errorf("service %q: render next-steps: %w", s.ID, err)
	}
	return buf.String(), nil
}
