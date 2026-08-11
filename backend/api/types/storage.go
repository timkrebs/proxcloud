package types

// StorageSummary is one storage row in the cluster-wide storage list
// (from /cluster/resources type=storage). Sizes are raw bytes.
type StorageSummary struct {
	Storage string   `json:"storage"`
	Node    string   `json:"node"`
	Type    string   `json:"type"`
	Content []string `json:"content"`
	Active  bool     `json:"active"`
	Shared  bool     `json:"shared"`
	Used    int64    `json:"used"`
	Total   int64    `json:"total"`
}

// NodeStorage is one storage as seen from a specific node
// (GET /nodes/{node}/storage). Sizes are raw bytes.
type NodeStorage struct {
	Storage string   `json:"storage"`
	Type    string   `json:"type"`
	Content []string `json:"content"`
	Active  bool     `json:"active"`
	Enabled bool     `json:"enabled"`
	Shared  bool     `json:"shared"`
	Used    int64    `json:"used"`
	Total   int64    `json:"total"`
	Avail   int64    `json:"avail"`
}

// StorageContentItem is one volume on a storage (ISO, container template,
// backup, disk image — GET /nodes/{node}/storage/{storage}/content).
type StorageContentItem struct {
	VolID     string `json:"volid"`
	Format    string `json:"format"`
	SizeBytes int64  `json:"sizeBytes"`
	Notes     string `json:"notes,omitempty"`
}
