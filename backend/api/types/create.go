package types

import "time"

// CreateSource selects where the new guest comes from.
type CreateSource struct {
	Mode string `json:"mode"` // "iso" | "image" | "vztmpl" | "clone"
	// ISOVolID attaches a bootable install ISO as a CD-ROM (installer flow).
	ISOVolID string `json:"isoVolId,omitempty"`
	// ImageVolID imports a cloud/disk image as the boot DISK (import-from) — the
	// only path that boots a raw cloud .img and runs cloud-init. A cloud image is
	// NOT a bootable CD-ROM; the catalog uses this mode.
	ImageVolID  string `json:"imageVolId,omitempty"`
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

	// Catalog, when set, marks this as a service-catalog deployment (ADR-0025):
	// the guest's cloud-init user-data comes from a rendered snippet referenced by
	// cicustom, NOT from the inline ciuser/cipassword/sshkeys (PVE drops those when
	// cicustom user= is set). The snippet content itself is carried into the deploy
	// engine out-of-band (deploy.CreateContext), never in this request body.
	Catalog *CatalogProvision `json:"catalog,omitempty"`
}

// CatalogProvision is the catalog-specific create parameters. It carries the
// cicustom snippet reference and the non-secret display metadata (ports, a
// credential hint) that the deployment surfaces once the guest is ready. It
// never carries a credential value.
type CatalogProvision struct {
	ServiceID      string `json:"serviceId"`
	SnippetRef     string `json:"snippetRef"` // "<datastore>:snippets/<file>"
	Ports          []int  `json:"ports,omitempty"`
	CredentialHint string `json:"credentialHint,omitempty"`
	UserSupplied   bool   `json:"userSupplied"`
}

// ProvisionServiceRequest is the body of POST
// /tenants/{tenantId}/service-catalog/{serviceId}/provision. Sizing fields left
// zero fall back to the service definition's default. In Phase A the credential
// is always generated server-side (the user-supplied path is Phase C).
type ProvisionServiceRequest struct {
	ProjectId string    `json:"projectId"`
	Name      string    `json:"name"`
	Node      string    `json:"node"`
	VMID      int       `json:"vmid"`
	Cores     int       `json:"cores,omitempty"`
	MemoryMB  int64     `json:"memoryMb,omitempty"`
	DiskGB    int       `json:"diskGb,omitempty"`
	Storage   string    `json:"storage"`
	Bridge    string    `json:"bridge"`
	VLANTag   int       `json:"vlanTag,omitempty"`
	Firewall  bool      `json:"firewall"`
	IPConfig  *IPConfig `json:"ipConfig,omitempty"`
	SSHKeys   []string  `json:"sshKeys,omitempty"`
	Tags      []string  `json:"tags,omitempty"`
}

// ProvisionServiceResponse acknowledges an accepted service deployment. A
// generated credential is surfaced HERE exactly once (per the secrets-server-side
// iron rule, ADR-0028) — it is never stored, logged, audited, or returned again.
type ProvisionServiceResponse struct {
	DeploymentID string `json:"deploymentId"`
	VMID         int    `json:"vmid"`
	// Username / GeneratedPassword are the one-time generated credential. Empty on
	// the (Phase C) user-supplied path.
	Username          string `json:"username,omitempty"`
	GeneratedPassword string `json:"generatedPassword,omitempty"`
}

// CreateGuestResponse acknowledges an accepted deployment.
type CreateGuestResponse struct {
	DeploymentID string `json:"deploymentId"`
	VMID         int    `json:"vmid"`
}

// DeploymentStep is one row of the deployment progress table.
type DeploymentStep struct {
	Key       string     `json:"key"`    // prepare | create | start | configuring
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

	// Catalog service connection details, populated by the deploy engine's
	// `configuring` step once a catalog guest is ready (ADR-0028). All omitempty,
	// so the bare-guest path is unchanged. None of these carry a secret.
	Connection     string `json:"connection,omitempty"`     // reachable host:port
	Ports          []int  `json:"ports,omitempty"`          // exposed service ports
	CredentialHint string `json:"credentialHint,omitempty"` // NON-secret auth hint
}
