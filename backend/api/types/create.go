package types

import "time"

// CreateSource selects where the new guest comes from.
type CreateSource struct {
	Mode        string `json:"mode"` // "iso" | "vztmpl" | "clone"
	ISOVolID    string `json:"isoVolId,omitempty"`
	VztmplVolID string `json:"vztmplVolId,omitempty"`
	CloneVMID   int    `json:"cloneVmid,omitempty"`
	CloneNode   string `json:"cloneNode,omitempty"`
	CloneMode   string `json:"cloneMode,omitempty"` // "full" | "linked"
}

// IPConfig is the optional cloud-init / lxc network address config.
type IPConfig struct {
	Mode    string `json:"mode"`              // "dhcp" | "static"
	CIDR    string `json:"cidr,omitempty"`    // 192.168.1.50/24
	Gateway string `json:"gateway,omitempty"` // 192.168.1.1
}

// CloudInitRequest carries the cloud-init (qemu) / provisioning (lxc)
// account settings.
type CloudInitRequest struct {
	User         string   `json:"user,omitempty"`     // qemu ciuser
	Password     string   `json:"password,omitempty"` // qemu cipassword / lxc root password
	SSHKeys      []string `json:"sshKeys,omitempty"`
	Nameserver   string   `json:"nameserver,omitempty"`
	SearchDomain string   `json:"searchDomain,omitempty"`
}

// CreateGuestRequest is the wizard's submit payload.
type CreateGuestRequest struct {
	Type string `json:"type"` // "qemu" | "lxc"
	Name string `json:"name"` // qemu: DNS name; lxc: hostname
	Node string `json:"node"`
	VMID int    `json:"vmid"`

	// ProjectId is required (Phase 3): the backend derives the Proxmox pool from
	// it. Replaces the old client-supplied Pool, which is now server-derived.
	ProjectId string `json:"projectId"`
	// Pool is deprecated and ignored on input — the handler overwrites it with
	// the project's pool before the request reaches the deploy engine.
	Pool string `json:"pool,omitempty"`

	Source CreateSource `json:"source"`

	Cores    int    `json:"cores"`
	MemoryMB int64  `json:"memoryMb"`
	DiskGB   int    `json:"diskGb"`
	Storage  string `json:"storage"`

	Bridge   string    `json:"bridge"`
	VLANTag  int       `json:"vlanTag,omitempty"`
	Firewall bool      `json:"firewall"`
	IPConfig *IPConfig `json:"ipConfig,omitempty"`

	CloudInit *CloudInitRequest `json:"cloudInit,omitempty"`
	Tags      []string          `json:"tags,omitempty"`

	StartAfterCreate bool `json:"startAfterCreate"`
}

// CreateGuestResponse acknowledges an accepted deployment.
type CreateGuestResponse struct {
	DeploymentID string `json:"deploymentId"`
	VMID         int    `json:"vmid"`
}

// DeploymentStep is one row of the deployment progress table.
type DeploymentStep struct {
	Key       string     `json:"key"`    // create | start
	Label     string     `json:"label"`  // "Create virtual machine web-02"
	Status    string     `json:"status"` // pending | running | succeeded | failed
	UPID      string     `json:"upid,omitempty"`
	Message   string     `json:"message,omitempty"` // verbatim PVE error on failure
	StartedAt *time.Time `json:"startedAt,omitempty"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`
}

// Deployment is a wizard-initiated provisioning run.
type Deployment struct {
	ID        string           `json:"id"`
	Name      string           `json:"name"`
	Type      string           `json:"type"` // qemu | lxc
	Node      string           `json:"node"`
	VMID      int              `json:"vmid"`
	Status    string           `json:"status"` // running | succeeded | failed
	CreatedAt time.Time        `json:"createdAt"`
	Steps     []DeploymentStep `json:"steps"`
}
