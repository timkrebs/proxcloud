package types

// Health is the /api/health response.
type Health struct {
	Status  string `json:"status"`            // ok
	Proxmox string `json:"proxmox,omitempty"` // ok | unreachable (populated once the PVE client is wired)
}
