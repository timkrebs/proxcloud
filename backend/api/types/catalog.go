package types

// CatalogService is the frontend-facing view of a catalog service definition
// (ADR-0026). It deliberately omits the internal baseImage ref and the
// cloud-init template — the gallery renders metadata + sizing + input schema
// only. Credential values are never present here (this is a definition, not an
// instance).
type CatalogService struct {
	ID          string              `json:"id"`
	DisplayName string              `json:"displayName"`
	Description string              `json:"description"`
	Icon        string              `json:"icon"`
	Category    string              `json:"category"`
	Kind        string              `json:"kind"`      // single | set
	GuestType   string              `json:"guestType"` // qemu
	Sizing      CatalogSizing       `json:"sizing"`
	Credentials []CatalogCredential `json:"credentials"`
	Ports       []int               `json:"ports"`
	Readiness   string              `json:"readiness"`
	Docs        string              `json:"docs"`
	TestedOn    string              `json:"testedOn"`
}

// CatalogSizing is the wizard default + minimum floor for a service.
type CatalogSizing struct {
	Default CatalogSize `json:"default"`
	Min     CatalogSize `json:"min"`
}

// CatalogSize is one sizing triple.
type CatalogSize struct {
	Cores    int   `json:"cores"`
	MemoryMB int64 `json:"memoryMb"`
	DiskGB   int   `json:"diskGb"`
}

// CatalogCredential describes one credential input the service accepts. It never
// carries a value — only whether the wizard exposes/generates it.
type CatalogCredential struct {
	Name             string `json:"name"`
	Username         string `json:"username,omitempty"`
	UsernameSettable bool   `json:"usernameSettable"`
	UserSettable     bool   `json:"userSettable"`
	GeneratedDefault bool   `json:"generatedDefault"`
}

// CatalogServiceList is the gallery payload.
type CatalogServiceList struct {
	Services []CatalogService `json:"services"`
}
