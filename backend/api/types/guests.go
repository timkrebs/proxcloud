package types

import "time"

// NICConfig is one parsed netN config entry of a guest.
type NICConfig struct {
	Key      string `json:"key"` // net0, net1, ...
	Model    string `json:"model"`
	MAC      string `json:"mac"`
	Bridge   string `json:"bridge"`
	VLANTag  int    `json:"vlanTag,omitempty"`
	Firewall bool   `json:"firewall"`
	IPConfig string `json:"ipConfig,omitempty"` // lxc: ip=dhcp / CIDR from the same entry
}

// DiskConfig is one parsed disk entry (scsiN/virtioN/sataN/ideN/rootfs/mpN).
type DiskConfig struct {
	Key       string `json:"key"`
	Storage   string `json:"storage"`
	Volume    string `json:"volume"`
	Format    string `json:"format,omitempty"`
	SizeBytes int64  `json:"sizeBytes"`
	CDROM     bool   `json:"cdrom"`
}

// GuestDetail is the full detail-page document for one guest. Flattened
// (no embedding) because tygo renders embedded structs as named fields,
// which would diverge from Go's inline JSON marshaling.
type GuestDetail struct {
	ID              string   `json:"id"`
	Type            string   `json:"type"`
	VMID            int      `json:"vmid"`
	Name            string   `json:"name"`
	Node            string   `json:"node"`
	Status          string   `json:"status"`
	UptimeSec       int64    `json:"uptimeSec"`
	CPUPct          float64  `json:"cpuPct"`
	Cores           int      `json:"cores"`
	MemUsed         int64    `json:"memUsed"`
	MemMax          int64    `json:"memMax"`
	DiskMax         int64    `json:"diskMax"`
	Pool            string   `json:"pool"`
	Tags            []string `json:"tags"`
	Template        bool     `json:"template"`
	PendingTaskUPID string   `json:"pendingTaskUpid,omitempty"`

	Description string       `json:"description,omitempty"`
	Agent       bool         `json:"agent"` // qemu: agent enabled in config
	OnBoot      bool         `json:"onBoot"`
	OSType      string       `json:"osType,omitempty"`
	Machine     string       `json:"machine,omitempty"` // qemu machine type
	BootDisk    string       `json:"bootDisk,omitempty"`
	NICs        []NICConfig  `json:"nics"`
	Disks       []DiskConfig `json:"disks"`
	DiskRead    int64        `json:"diskRead"`  // lifetime bytes
	DiskWrite   int64        `json:"diskWrite"` // lifetime bytes
	NetIn       int64        `json:"netIn"`     // lifetime bytes
	NetOut      int64        `json:"netOut"`    // lifetime bytes
}

// UpdateConfigRequest is the PATCH /config body; nil fields are unchanged.
type UpdateConfigRequest struct {
	Cores       *int      `json:"cores,omitempty"`
	MemoryMB    *int64    `json:"memoryMb,omitempty"`
	Description *string   `json:"description,omitempty"`
	Tags        *[]string `json:"tags,omitempty"`
	OnBoot      *bool     `json:"onBoot,omitempty"`
}

// ResizeRequest grows a disk to an absolute size.
type ResizeRequest struct {
	Disk    string `json:"disk"` // e.g. scsi0, rootfs
	SizeGiB int    `json:"sizeGib"`
}

// Snapshot is one guest snapshot.
type Snapshot struct {
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	Parent      string    `json:"parent,omitempty"`
	SnapTime    time.Time `json:"snapTime"`
	VMState     bool      `json:"vmState"`
	Current     bool      `json:"current"` // the "you are here" marker
}

// CreateSnapshotRequest is the POST /snapshots body.
type CreateSnapshotRequest struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	VMState     bool   `json:"vmState"`
}

// GuestNIC is one live interface reported by the guest (agent for qemu,
// the interfaces endpoint for lxc).
type GuestNIC struct {
	Name string   `json:"name"`
	MAC  string   `json:"mac,omitempty"`
	IPv4 []string `json:"ipv4"`
	IPv6 []string `json:"ipv6"`
}

// GuestNICList wraps live interfaces; AgentUnavailable=true is the honest
// "no QEMU guest agent" state (install hint in the UI, never blank).
type GuestNICList struct {
	AgentUnavailable bool       `json:"agentUnavailable"`
	NICs             []GuestNIC `json:"nics"`
}

// FirewallRule is one guest firewall rule.
type FirewallRule struct {
	Pos     int    `json:"pos"`
	Enable  bool   `json:"enable"`
	Type    string `json:"type"` // in | out | group
	Action  string `json:"action"`
	Source  string `json:"source,omitempty"`
	Dest    string `json:"dest,omitempty"`
	Proto   string `json:"proto,omitempty"`
	DPort   string `json:"dport,omitempty"`
	SPort   string `json:"sport,omitempty"`
	Comment string `json:"comment,omitempty"`
}

// GuestFirewall is the firewall blade document.
type GuestFirewall struct {
	Enabled bool           `json:"enabled"`
	Rules   []FirewallRule `json:"rules"`
}

// ACLEntry is one row of the access-control blade (read-only in v1).
type ACLEntry struct {
	Path      string `json:"path"`
	UGID      string `json:"ugid"`
	Type      string `json:"type"` // user | group | token
	Role      string `json:"role"`
	Propagate bool   `json:"propagate"`
}
