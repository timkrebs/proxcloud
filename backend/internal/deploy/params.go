// Package deploy turns a wizard CreateGuestRequest into real Proxmox API
// calls: parameter assembly (pure, heavily tested) and the step engine
// that runs create → start with live progress.
package deploy

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

var (
	// qemu names are DNS names; lxc hostnames the same. Both must satisfy
	// the wizard rule: start lowercase letter, then lowercase/digits/hyphens.
	nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	tagRe  = regexp.MustCompile(`^[a-z0-9_][a-z0-9_.-]*$`)
	cidrRe = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}/\d{1,2}$`)
	ipRe   = regexp.MustCompile(`^\d{1,3}(\.\d{1,3}){3}$`)

	// pveIDRe guards identifiers spliced into PVE URLs and composite
	// config values (node, storage, bridge, pool): no commas, equals,
	// slashes, or dot segments can sneak extra options into a parameter.
	pveIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	// volIDRe matches "storage:path/to/volume" content volume ids.
	volIDRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*:[A-Za-z0-9][A-Za-z0-9._/-]*$`)
	// hostnameRe for nameserver/searchdomain values.
	hostnameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.:-]{0,253}$`)
)

func validPVEID(s string) bool {
	return s != "." && s != ".." && pveIDRe.MatchString(s)
}

// Validate checks the request before anything touches Proxmox; the wizard
// mirrors these rules client-side.
func Validate(req *types.CreateGuestRequest) error {
	fail := func(format string, a ...any) error { return fmt.Errorf(format, a...) }

	if req.Type != "qemu" && req.Type != "lxc" {
		return fail("type must be qemu or lxc")
	}
	if !nameRe.MatchString(req.Name) {
		return fail("name must start with a lowercase letter and contain only lowercase letters, digits, and hyphens (max 40)")
	}
	if !validPVEID(req.Node) {
		return fail("node must be a valid PVE node name")
	}
	if req.VMID < 100 || req.VMID > 999999999 {
		return fail("vmid must be between 100 and 999999999")
	}
	if req.Cores < 1 || req.Cores > 128 {
		return fail("cores must be between 1 and 128")
	}
	if req.MemoryMB < 128 {
		return fail("memory must be at least 128 MiB")
	}

	switch req.Source.Mode {
	case "vztmpl":
		if req.Type != "lxc" {
			return fail("vztmpl source is only valid for lxc")
		}
		if req.Source.VztmplVolID == "" {
			return fail("template volume is required")
		}
		if !volIDRe.MatchString(req.Source.VztmplVolID) {
			return fail("template volume id has an invalid format")
		}
	case "iso":
		if req.Type != "qemu" {
			return fail("iso source is only valid for qemu")
		}
		if req.Source.ISOVolID == "" {
			return fail("ISO volume is required")
		}
		if !volIDRe.MatchString(req.Source.ISOVolID) {
			return fail("ISO volume id has an invalid format")
		}
	case "clone":
		if req.Type != "qemu" {
			return fail("clone source is only valid for qemu in v1")
		}
		if req.Source.CloneVMID < 100 {
			return fail("clone source VMID is required")
		}
		if req.Source.CloneMode != "full" && req.Source.CloneMode != "linked" {
			return fail("clone mode must be full or linked")
		}
	default:
		return fail("source mode must be iso, vztmpl, or clone")
	}

	if req.Source.Mode != "clone" {
		if req.DiskGB < 1 {
			return fail("disk size must be at least 1 GiB")
		}
		if !validPVEID(req.Storage) {
			return fail("storage must be a valid PVE storage name")
		}
		if !validPVEID(req.Bridge) {
			return fail("bridge must be a valid interface name")
		}
	} else if req.Storage != "" && !validPVEID(req.Storage) {
		return fail("storage must be a valid PVE storage name")
	}
	if req.Pool != "" && !validPVEID(req.Pool) {
		return fail("pool must be a valid PVE pool name")
	}
	if req.VLANTag < 0 || req.VLANTag > 4094 {
		return fail("vlan tag must be between 1 and 4094")
	}
	if req.IPConfig != nil && req.IPConfig.Mode == "static" {
		if !cidrRe.MatchString(req.IPConfig.CIDR) {
			return fail("static IP must be CIDR notation, e.g. 192.168.1.50/24")
		}
		if req.IPConfig.Gateway != "" && !ipRe.MatchString(req.IPConfig.Gateway) {
			return fail("gateway must be an IPv4 address")
		}
	}
	for _, t := range req.Tags {
		if !tagRe.MatchString(t) {
			return fail("invalid tag %q — lowercase letters, digits, . - _ only", t)
		}
	}
	if ci := req.CloudInit; ci != nil {
		if ci.Nameserver != "" && !hostnameRe.MatchString(ci.Nameserver) {
			return fail("nameserver has an invalid format")
		}
		if ci.SearchDomain != "" && !hostnameRe.MatchString(ci.SearchDomain) {
			return fail("search domain has an invalid format")
		}
	}
	return nil
}

// net0String assembles the netN parameter for either guest type.
func net0String(req *types.CreateGuestRequest) string {
	var parts []string
	if req.Type == "qemu" {
		parts = append(parts, "virtio")
	} else {
		parts = append(parts, "name=eth0")
	}
	parts = append(parts, "bridge="+req.Bridge)
	if req.VLANTag > 0 {
		parts = append(parts, fmt.Sprintf("tag=%d", req.VLANTag))
	}
	if req.Firewall {
		parts = append(parts, "firewall=1")
	}
	if req.Type == "lxc" {
		// lxc carries its address config inside net0.
		if req.IPConfig != nil && req.IPConfig.Mode == "static" {
			parts = append(parts, "ip="+req.IPConfig.CIDR)
			if req.IPConfig.Gateway != "" {
				parts = append(parts, "gw="+req.IPConfig.Gateway)
			}
		} else {
			parts = append(parts, "ip=dhcp")
		}
	}
	return strings.Join(parts, ",")
}

// BuildCreateParams assembles the PVE create-call parameters. Clone mode
// is handled by BuildCloneParams instead.
func BuildCreateParams(req *types.CreateGuestRequest) (map[string]any, error) {
	if err := Validate(req); err != nil {
		return nil, err
	}
	if req.Source.Mode == "clone" {
		return nil, fmt.Errorf("clone requests use BuildCloneParams")
	}

	p := map[string]any{
		"vmid":   req.VMID,
		"cores":  req.Cores,
		"memory": req.MemoryMB,
		"net0":   net0String(req),
	}
	if req.Pool != "" {
		p["pool"] = req.Pool
	}
	if len(req.Tags) > 0 {
		p["tags"] = strings.Join(req.Tags, ";")
	}

	if req.Type == "lxc" {
		p["hostname"] = req.Name
		p["ostemplate"] = req.Source.VztmplVolID
		p["rootfs"] = fmt.Sprintf("%s:%d", req.Storage, req.DiskGB)
		p["unprivileged"] = 1
		if ci := req.CloudInit; ci != nil {
			if ci.Password != "" {
				p["password"] = ci.Password
			}
			if len(ci.SSHKeys) > 0 {
				p["ssh-public-keys"] = strings.Join(ci.SSHKeys, "\n")
			}
			if ci.Nameserver != "" {
				p["nameserver"] = ci.Nameserver
			}
			if ci.SearchDomain != "" {
				p["searchdomain"] = ci.SearchDomain
			}
		}
		return p, nil
	}

	// qemu from ISO
	p["name"] = req.Name
	p["ostype"] = "l26"
	p["scsihw"] = "virtio-scsi-single"
	p["scsi0"] = fmt.Sprintf("%s:%d", req.Storage, req.DiskGB)
	p["ide2"] = req.Source.ISOVolID + ",media=cdrom"
	p["boot"] = "order=scsi0;ide2"
	p["agent"] = 1

	if ci := req.CloudInit; ci != nil {
		// Cloud-init drive on ide0 (ide2 holds the installer ISO).
		p["ide0"] = req.Storage + ":cloudinit"
		if ci.User != "" {
			p["ciuser"] = ci.User
		}
		if ci.Password != "" {
			p["cipassword"] = ci.Password
		}
		if len(ci.SSHKeys) > 0 {
			// PVE requires the sshkeys parameter URL-encoded.
			p["sshkeys"] = url.QueryEscape(strings.Join(ci.SSHKeys, "\n"))
		}
		if ci.Nameserver != "" {
			p["nameserver"] = ci.Nameserver
		}
		if ci.SearchDomain != "" {
			p["searchdomain"] = ci.SearchDomain
		}
		if req.IPConfig != nil {
			if req.IPConfig.Mode == "static" {
				ip := "ip=" + req.IPConfig.CIDR
				if req.IPConfig.Gateway != "" {
					ip += ",gw=" + req.IPConfig.Gateway
				}
				p["ipconfig0"] = ip
			} else {
				p["ipconfig0"] = "ip=dhcp"
			}
		}
	}
	return p, nil
}

// CloneParams is the digested clone call.
type CloneParams struct {
	NewVMID int
	Name    string
	Pool    string
	Full    bool
	Storage string // only valid for full clones
	Target  string // target node (same-node in v1)
}

// BuildCloneParams assembles the PVE clone-call parameters.
func BuildCloneParams(req *types.CreateGuestRequest) (CloneParams, error) {
	if err := Validate(req); err != nil {
		return CloneParams{}, err
	}
	cp := CloneParams{
		NewVMID: req.VMID,
		Name:    req.Name,
		Pool:    req.Pool,
		Full:    req.Source.CloneMode == "full",
	}
	// Linked clones must stay on the source storage — PVE rejects a
	// storage parameter for them.
	if cp.Full && req.Storage != "" {
		cp.Storage = req.Storage
	}
	return cp, nil
}
