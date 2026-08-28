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

// ProvisionCredential carries ONE user-supplied credential for a service, keyed
// by the service's declared credential name (see CatalogCredential.Name). It is
// present only when the user chose "I'll set it" for that credential; a credential
// with no entry here falls back to server-side crypto/rand generation (Phase A).
//
// The values are attacker-influenced and validated server-side (ADR-0027 §3:
// length-only password policy, ≥ 12 chars; username against the credential's
// allowed charset only when usernameSettable). They are injected through the SAME
// mandatory base64 transport as a generated value (§1) — the raw bytes never touch
// YAML or a shell string — and are never logged, stored, audited, or echoed back.
type ProvisionCredential struct {
	Name     string `json:"name"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
}

// ProvisionServiceRequest is the body of POST
// /tenants/{tenantId}/service-catalog/{serviceId}/provision. Sizing fields left
// zero fall back to the service definition's default. Credentials left absent are
// generated server-side (Phase A); a Credentials entry lets the user SUPPLY a
// credential (Phase C), validated server-authoritatively before any reservation.
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

	// Credentials carries user-supplied credential values (Phase C). Omit an entry
	// to have that credential generated. A supplied password must be ≥ 12 chars; a
	// supplied username is only accepted when the credential is usernameSettable.
	Credentials []ProvisionCredential `json:"credentials,omitempty"`
}

// ProvisionServiceResponse acknowledges an accepted service deployment. A
// generated credential is surfaced HERE exactly once (per the secrets-server-side
// iron rule, ADR-0028) — it is never stored, logged, audited, or returned again.
type ProvisionServiceResponse struct {
	DeploymentID string `json:"deploymentId"`
	VMID         int    `json:"vmid"`
	// Username is the credential's account/role name. GeneratedPassword is set ONLY
	// when the credential was generated server-side (the one-time reveal); it is
	// EMPTY when the user supplied the credential — the user already has that value,
	// so it is never echoed back.
	Username          string `json:"username,omitempty"`
	GeneratedPassword string `json:"generatedPassword,omitempty"`
	// CredentialHint is a NON-secret indicator of the credential's origin:
	// "generated — shown once" when GeneratedPassword is set, or "you set this
	// credential" when the user supplied it. It never contains a credential value.
	CredentialHint string `json:"credentialHint,omitempty"`
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
