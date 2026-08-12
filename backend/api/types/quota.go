package types

// QuotaLimits are a scope's per-dimension caps. A nil pointer means unlimited on
// that dimension (the frontend renders "Unlimited", no bar).
type QuotaLimits struct {
	MaxVCPU   *int   `json:"maxVcpu"`
	MaxRAMMB  *int64 `json:"maxRamMb"`
	MaxDiskGB *int64 `json:"maxDiskGb"`
	MaxCount  *int   `json:"maxCount"`
}

// QuotaUsage is a scope's live usage. It sums PROVISIONED allocations (ADR-0012
// §4): active guests from ClusterResources (MaxCPU / MaxMem / MaxDisk) plus
// pending reservations from resource_ownership.reserved_*.
type QuotaUsage struct {
	VCPU   int   `json:"vcpu"`
	RAMMB  int64 `json:"ramMb"`
	DiskGB int64 `json:"diskGb"`
	Count  int   `json:"count"`
}

// QuotaWithUsage is one scope's limits + live usage + remaining. Remaining on a
// dimension is meaningful only where the matching limit is non-null (0 elsewhere).
type QuotaWithUsage struct {
	ScopeType string      `json:"scopeType"` // "tenant" | "project"
	ScopeID   string      `json:"scopeId"`
	Limits    QuotaLimits `json:"limits"`
	Usage     QuotaUsage  `json:"usage"`
	Remaining QuotaUsage  `json:"remaining"`
}

// SetQuotaRequest is the PUT body for tenant/project quota. Each nil field clears
// that limit (→ unlimited).
type SetQuotaRequest struct {
	MaxVCPU   *int   `json:"maxVcpu"`
	MaxRAMMB  *int64 `json:"maxRamMb"`
	MaxDiskGB *int64 `json:"maxDiskGb"`
	MaxCount  *int   `json:"maxCount"`
}

// ProjectQuotaResponse embeds the parent tenant rollup so the wizard binds on the
// tighter of the two — min(projectRemaining, tenantRemaining) — in one round-trip.
type ProjectQuotaResponse struct {
	Project QuotaWithUsage `json:"project"`
	Tenant  QuotaWithUsage `json:"tenant"`
}
