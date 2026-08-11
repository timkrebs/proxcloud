package types

// NodeSummary is one row of the GET /api/nodes list.
// Sizes are raw bytes; CPUPct is 0-100.
type NodeSummary struct {
	Node       string    `json:"node"`
	Online     bool      `json:"online"`
	CPUPct     float64   `json:"cpuPct"`
	MemUsed    int64     `json:"memUsed"`
	MemTotal   int64     `json:"memTotal"`
	DiskUsed   int64     `json:"diskUsed"`
	DiskTotal  int64     `json:"diskTotal"`
	UptimeSec  int64     `json:"uptimeSec"`
	PVEVersion string    `json:"pveVersion"`
	LoadAvg    []float64 `json:"loadAvg"`
}

// NodeDetail is the GET /api/nodes/{node} response — a strict superset of
// NodeSummary (fields spelled out flat so the generated TS stays flat).
type NodeDetail struct {
	Node       string    `json:"node"`
	Online     bool      `json:"online"`
	CPUPct     float64   `json:"cpuPct"`
	MemUsed    int64     `json:"memUsed"`
	MemTotal   int64     `json:"memTotal"`
	DiskUsed   int64     `json:"diskUsed"`
	DiskTotal  int64     `json:"diskTotal"`
	UptimeSec  int64     `json:"uptimeSec"`
	PVEVersion string    `json:"pveVersion"`
	LoadAvg    []float64 `json:"loadAvg"`

	KernelVersion string `json:"kernelVersion"`
	CPUModel      string `json:"cpuModel"`
	CPUCores      int    `json:"cpuCores"`
	CPUSockets    int    `json:"cpuSockets"`
	SwapUsed      int64  `json:"swapUsed"`
	SwapTotal     int64  `json:"swapTotal"`
}
