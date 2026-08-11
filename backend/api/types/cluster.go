package types

// ClusterSummary is the GET /api/cluster response: one card's worth of
// cluster-wide truth, aggregated from /cluster/status and /cluster/resources.
type ClusterSummary struct {
	Name        string       `json:"name"`
	Quorate     bool         `json:"quorate"`
	PVEVersion  string       `json:"pveVersion"`
	NodesOnline int          `json:"nodesOnline"`
	NodesTotal  int          `json:"nodesTotal"`
	Guests      GuestCounts  `json:"guests"`
	Usage       ClusterUsage `json:"usage"`
}

// GuestCounts tallies guests by type and run state across the cluster.
type GuestCounts struct {
	VMsRunning  int `json:"vmsRunning"`
	VMsTotal    int `json:"vmsTotal"`
	LXCsRunning int `json:"lxcsRunning"`
	LXCsTotal   int `json:"lxcsTotal"`
}

// ClusterUsage aggregates resource consumption over all online nodes.
// Sizes are raw bytes; CPUPct is 0-100.
type ClusterUsage struct {
	CPUPct    float64 `json:"cpuPct"`
	MemUsed   int64   `json:"memUsed"`
	MemTotal  int64   `json:"memTotal"`
	DiskUsed  int64   `json:"diskUsed"`
	DiskTotal int64   `json:"diskTotal"`
}
