package types

// GuestSummary is one guest (VM or LXC container) in the "All resources"
// list, derived from a /cluster/resources row. Sizes are raw bytes; CPUPct
// is 0-100. Status uses the canonical lowercase vocabulary:
// running | stopped, plus transitional provisioning | starting | stopping |
// restarting | resizing | deleting.
type GuestSummary struct {
	ID        string   `json:"id"`   // "qemu/101" or "lxc/200"
	Type      string   `json:"type"` // qemu | lxc
	VMID      int      `json:"vmid"`
	Name      string   `json:"name"`
	Node      string   `json:"node"`
	Status    string   `json:"status"`
	UptimeSec int64    `json:"uptimeSec"`
	CPUPct    float64  `json:"cpuPct"`
	Cores     int      `json:"cores"`
	MemUsed   int64    `json:"memUsed"`
	MemMax    int64    `json:"memMax"`
	DiskMax   int64    `json:"diskMax"`
	Pool      string   `json:"pool"`
	Tags      []string `json:"tags"`
	Template  bool     `json:"template"`

	// PendingTaskUPID is set while a Proxcloud-initiated task is running
	// against this guest; Status then carries the transitional value.
	PendingTaskUPID string `json:"pendingTaskUpid,omitempty"`
}
