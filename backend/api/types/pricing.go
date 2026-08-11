package types

// Pricing is the flat-rate config the UI uses to compute honest estimates.
// Enabled=false hides all cost UI.
type Pricing struct {
	Enabled     bool    `json:"enabled"`
	Currency    string  `json:"currency"`
	VCPUMonth   float64 `json:"vcpuMonth"`
	RAMGBMonth  float64 `json:"ramGbMonth"`
	DiskGBMonth float64 `json:"diskGbMonth"`
}
