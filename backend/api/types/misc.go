package types

// NextID is the GET /api/next-id response: the next free VMID in the cluster.
type NextID struct {
	VMID int `json:"vmid"`
}

// Bridge is one network bridge on a node, offered when attaching guest NICs.
type Bridge struct {
	Iface   string `json:"iface"`
	Active  bool   `json:"active"`
	Comment string `json:"comment,omitempty"`
}
