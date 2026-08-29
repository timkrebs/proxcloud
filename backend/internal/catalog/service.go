// Package catalog is the service catalog: a curated set of provisioning recipes
// ("a Postgres", "a Redis") loaded from embedded definition files at startup,
// validated fail-fast, and rendered into cloud-init user-data + post-ready
// next-steps. It is the platform (global) catalog tier — definitions carry no
// tenant and are the same for every tenant (ADR-0026).
//
// The security-critical seam is render.go: it turns server-authoritative inputs
// (host, port, base64-encoded credentials) into the #cloud-config the deploy
// engine uploads as a snippet. See docs/proxmox/cloud-init.md §4 for why every
// untrusted value is base64-transported and decoded in-guest.
package catalog

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"text/template"
)

// idRe is the stable-slug rule for a service id (and its directory name).
var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,39}$`)

// volIDRe matches a storage-scoped volume id, mirroring deploy.volIDRe so a
// baseImage.ref that the create path will accept is validated at load time.
var volIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*:[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// Size is a guest sizing triple.
type Size struct {
	Cores    int   `yaml:"cores"`
	MemoryMb int64 `yaml:"memoryMb"`
	DiskGb   int   `yaml:"diskGb"`
}

// BaseImage references the image a service's guest is created from.
type BaseImage struct {
	Ref string `yaml:"ref"`
}

// Sizing carries the wizard default and the minimum floor.
type Sizing struct {
	Default Size `yaml:"default"`
	Min     Size `yaml:"min"`
}

// RoleSpec is one member role of a kind:set service (ADR-0029/0030), e.g. the K3s
// 'server' (count 1) and 'agent' (count 2 default, adjustable within [Min,Max]).
// Each role has its own per-member Sizing. Name is the stable role slug and the
// prefix of the role's cloud-init template (<name>.cloud-init.yaml.tftpl).
type RoleSpec struct {
	Name   string `yaml:"name"`
	Count  int    `yaml:"count"` // wizard default member count for this role
	Min    int    `yaml:"min"`   // minimum members (0 → treated as Count, i.e. fixed)
	Max    int    `yaml:"max"`   // maximum members (0 → treated as Count, i.e. fixed)
	Sizing Sizing `yaml:"sizing"`
}

// CredentialSpec declares one credential the service injects via the snippet.
// UsernameSettable/UserSettable/GeneratedDefault drive the (Phase C) wizard;
// Phase A always generates.
type CredentialSpec struct {
	Name             string `yaml:"name"`
	Username         string `yaml:"username"`
	UsernameSettable bool   `yaml:"usernameSettable"`
	UserSettable     bool   `yaml:"userSettable"`
	GeneratedDefault bool   `yaml:"generatedDefault"`
}

// ServiceDef is one catalog service (ADR-0026). The unexported template fields
// are parsed once at load so a render never re-parses or touches the filesystem.
type ServiceDef struct {
	ID          string           `yaml:"id"`
	DisplayName string           `yaml:"displayName"`
	Description string           `yaml:"description"`
	Icon        string           `yaml:"icon"`
	Category    string           `yaml:"category"`
	Kind        string           `yaml:"kind"`      // single | set
	GuestType   string           `yaml:"guestType"` // qemu
	BaseImage   BaseImage        `yaml:"baseImage"`
	Sizing      Sizing           `yaml:"sizing"` // kind:single only (per-role sizing lives on Roles)
	Roles       []RoleSpec       `yaml:"roles"`  // kind:set only
	Credentials []CredentialSpec `yaml:"credentials"`
	Ports       []int            `yaml:"ports"`
	Readiness   string           `yaml:"readiness"` // tcp:<port>
	Docs        string           `yaml:"docs"`
	TestedOn    string           `yaml:"testedOn"`

	cloudInit     *template.Template            // kind:single: parsed cloud-init.yaml.tftpl
	roleCloudInit map[string]*template.Template // kind:set: role name -> parsed <role>.cloud-init.yaml.tftpl
	nextSteps     *template.Template            // parsed next-steps.md.tftpl
}

// IsSet reports whether the service provisions a multi-guest deployment set.
func (s *ServiceDef) IsSet() bool { return s.Kind == "set" }

// Role returns the RoleSpec with the given name, or (RoleSpec{}, false).
func (s *ServiceDef) Role(name string) (RoleSpec, bool) {
	for _, r := range s.Roles {
		if r.Name == name {
			return r, true
		}
	}
	return RoleSpec{}, false
}

// PrimaryPort returns the first declared port, or 0 when none.
func (s *ServiceDef) PrimaryPort() int {
	if len(s.Ports) == 0 {
		return 0
	}
	return s.Ports[0]
}

// ReadinessPort parses the `tcp:<port>` readiness target into a port number.
// ok is false when the target is absent or not a tcp:<port> form.
func (s *ServiceDef) ReadinessPort() (port int, ok bool) {
	rest, found := strings.CutPrefix(s.Readiness, "tcp:")
	if !found {
		return 0, false
	}
	p, err := strconv.Atoi(rest)
	if err != nil || p < 1 || p > 65535 {
		return 0, false
	}
	return p, true
}

// validate checks one definition; a returned error fails process startup. dir is
// the directory name the def was loaded from (must equal id).
func (s *ServiceDef) validate(dir string) error {
	if !idRe.MatchString(s.ID) {
		return fmt.Errorf("id %q must match [a-z0-9-] (max 40)", s.ID)
	}
	if s.ID != dir {
		return fmt.Errorf("id %q must equal its directory name %q", s.ID, dir)
	}
	if strings.TrimSpace(s.DisplayName) == "" {
		return fmt.Errorf("service %q: displayName is required", s.ID)
	}
	if strings.TrimSpace(s.Description) == "" {
		return fmt.Errorf("service %q: description is required", s.ID)
	}
	if strings.TrimSpace(s.Icon) == "" {
		return fmt.Errorf("service %q: icon is required", s.ID)
	}
	if strings.TrimSpace(s.Category) == "" {
		return fmt.Errorf("service %q: category is required", s.ID)
	}
	switch s.Kind {
	case "single":
		if len(s.Roles) > 0 {
			return fmt.Errorf("service %q: kind 'single' must not declare roles", s.ID)
		}
	case "set":
		// A set is provisioned as N linked guests grouped by role (ADR-0029). The
		// per-role member templates + sizing live on Roles; the top-level Sizing is
		// unused. Phase E implements this — it is no longer reserved.
		if len(s.Roles) == 0 {
			return fmt.Errorf("service %q: kind 'set' requires at least one role", s.ID)
		}
	default:
		return fmt.Errorf("service %q: kind must be 'single' or 'set' (got %q)", s.ID, s.Kind)
	}
	if s.GuestType != "qemu" {
		return fmt.Errorf("service %q: guestType must be 'qemu' (cloud-init user-data is VM-only; got %q)", s.ID, s.GuestType)
	}
	if !volIDRe.MatchString(s.BaseImage.Ref) {
		return fmt.Errorf("service %q: baseImage.ref %q is not a storage-scoped volume id", s.ID, s.BaseImage.Ref)
	}
	if s.IsSet() {
		if err := validateRoles(s.ID, s.Roles); err != nil {
			return err
		}
	} else if err := validateSizing(s.ID, s.Sizing); err != nil {
		return err
	}
	// An EMPTY credential schema is valid (ADR-0027 §4, the "Vault honesty" case):
	// a service whose real secrets are generated in-guest and never seen by
	// Proxcloud (e.g. Vault CE's unseal keys / root token) declares no credential,
	// so there is nothing to generate, inject, or reveal. Services that DO inject a
	// credential still validate each entry below.
	for i, c := range s.Credentials {
		if strings.TrimSpace(c.Name) == "" {
			return fmt.Errorf("service %q: credential[%d].name is required", s.ID, i)
		}
	}
	if len(s.Ports) == 0 {
		return fmt.Errorf("service %q: at least one port is required", s.ID)
	}
	for _, p := range s.Ports {
		if p < 1 || p > 65535 {
			return fmt.Errorf("service %q: port %d out of range", s.ID, p)
		}
	}
	if _, ok := s.ReadinessPort(); !ok {
		return fmt.Errorf("service %q: readiness must be 'tcp:<port>' (got %q)", s.ID, s.Readiness)
	}
	if strings.TrimSpace(s.Docs) == "" {
		return fmt.Errorf("service %q: docs link is required", s.ID)
	}
	if !dateRe.MatchString(s.TestedOn) {
		return fmt.Errorf("service %q: testedOn must be YYYY-MM-DD (got %q)", s.ID, s.TestedOn)
	}
	return nil
}

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// roleNameRe is the stable-slug rule for a role name (and its template prefix).
var roleNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)

// validateRoles checks a kind:set service's role schema: unique slug names, sane
// count/min/max bounds, and a valid per-role sizing. A returned error fails boot.
func validateRoles(id string, roles []RoleSpec) error {
	seen := map[string]bool{}
	for i, r := range roles {
		if !roleNameRe.MatchString(r.Name) {
			return fmt.Errorf("service %q: role[%d].name %q must match [a-z][a-z0-9-] (max 20)", id, i, r.Name)
		}
		if seen[r.Name] {
			return fmt.Errorf("service %q: duplicate role name %q", id, r.Name)
		}
		seen[r.Name] = true
		if r.Count < 1 {
			return fmt.Errorf("service %q: role %q count must be >= 1", id, r.Name)
		}
		// Min/Max default to Count (a fixed-size role) when left zero.
		minC, maxC := r.Min, r.Max
		if minC == 0 {
			minC = r.Count
		}
		if maxC == 0 {
			maxC = r.Count
		}
		if minC < 1 || maxC < minC || r.Count < minC || r.Count > maxC {
			return fmt.Errorf("service %q: role %q bounds invalid (min %d, default %d, max %d)", id, r.Name, minC, r.Count, maxC)
		}
		if err := validateSizing(id+"/"+r.Name, r.Sizing); err != nil {
			return err
		}
	}
	return nil
}

func validateSizing(id string, sz Sizing) error {
	dims := []struct {
		name string
		def  int64
		min  int64
	}{
		{"cores", int64(sz.Default.Cores), int64(sz.Min.Cores)},
		{"memoryMb", sz.Default.MemoryMb, sz.Min.MemoryMb},
		{"diskGb", int64(sz.Default.DiskGb), int64(sz.Min.DiskGb)},
	}
	for _, d := range dims {
		if d.min < 1 {
			return fmt.Errorf("service %q: sizing.min.%s must be >= 1", id, d.name)
		}
		if d.def < d.min {
			return fmt.Errorf("service %q: sizing.default.%s (%d) must be >= sizing.min.%s (%d)", id, d.name, d.def, d.name, d.min)
		}
	}
	return nil
}
