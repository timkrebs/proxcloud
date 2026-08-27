package types

import "time"

// Ttl is the wire representation of a guest's TTL / ephemeral-expiry (ADR-0020).
// Every value comes from the stored row. Action is "stop" (reversible: the guest
// is powered off and marked expired) or "delete" (irreversible destroy).
// OriginalDurationSeconds is the TTL length as chosen, used to size an extend.
type Ttl struct {
	ID                      string    `json:"id"`
	TenantID                string    `json:"tenantId"`
	ProjectID               string    `json:"projectId"`
	VMID                    int       `json:"vmid"`
	ExpiresAt               time.Time `json:"expiresAt"`
	Action                  string    `json:"action"` // "stop" | "delete"
	Warned24h               bool      `json:"warned24h"`
	Warned1h                bool      `json:"warned1h"`
	OriginalDurationSeconds int64     `json:"originalDurationSeconds"`
	CreatedAt               time.Time `json:"createdAt"`
	UpdatedAt               time.Time `json:"updatedAt"`
}

// TtlRequest is the PUT body for a guest TTL. The backend validates action,
// ttlSeconds (> 0 and <= the project max), and — when action is "delete" —
// requires confirmName to match the guest's name (typed-confirmation, enforced
// server-side: a destructive TTL cannot be set without naming the guest).
type TtlRequest struct {
	Action      string `json:"action"`                // "stop" | "delete"
	TtlSeconds  int64  `json:"ttlSeconds"`            // TTL length in seconds
	ConfirmName string `json:"confirmName,omitempty"` // required only when action == "delete"
}

// TtlPolicy is a project's TTL governance (ADR-0020). DefaultTtlSeconds is null
// when the project applies no default (a guest is permanent unless opted in);
// MaxTtlSeconds is the hard ceiling on any TTL at create or extend.
type TtlPolicy struct {
	DefaultTtlSeconds *int64 `json:"defaultTtlSeconds,omitempty"`
	MaxTtlSeconds     int64  `json:"maxTtlSeconds"`
}

// TtlPolicyRequest is the PUT body for a project TTL policy. A null
// defaultTtlSeconds clears the default (→ permanent by default).
type TtlPolicyRequest struct {
	DefaultTtlSeconds *int64 `json:"defaultTtlSeconds,omitempty"`
	MaxTtlSeconds     int64  `json:"maxTtlSeconds"`
}

// TtlExtendResult is the POST …/ttl/extend response: the new (capped) expiry.
type TtlExtendResult struct {
	ExpiresAt time.Time `json:"expiresAt"`
}

// TtlWarningEvent is the SSE "ttl_warning" payload: an advance heads-up (T-24h or
// T-1h) that a guest's TTL is about to fire. It carries the owning VMID so
// events.deliver can scope it to the owning tenant (it is NOT broadcast). Which
// is the warning tier ("24h" | "1h"); Action tells the UI whether the guest will
// be stopped or destroyed (so it can offer a one-click extend).
type TtlWarningEvent struct {
	VMID      int       `json:"vmid"`
	Node      string    `json:"node"`
	GuestType string    `json:"guestType"` // qemu | lxc
	Which     string    `json:"which"`     // "24h" | "1h"
	ExpiresAt time.Time `json:"expiresAt"`
	Action    string    `json:"action"` // "stop" | "delete"
}
