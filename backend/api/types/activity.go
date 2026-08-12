package types

import (
	"encoding/json"
	"time"
)

// ActivityEntry is one row of the merged activity timeline: the audit intent feed
// (Proxcloud actions) overlaid with the PVE task feed (raw Proxmox operations),
// both normalized to a common shape.
type ActivityEntry struct {
	ID          string          `json:"id"`     // audit uuid, or "task:"+upid
	Source      string          `json:"source"` // "audit" | "task"
	TS          time.Time       `json:"ts"`
	Actor       string          `json:"actor"`      // display name/email; "system" for reconciler
	Action      string          `json:"action"`     // "guest.create", "project.rename", PVE label…
	TargetType  string          `json:"targetType"` // "guest"|"project"|"tenant"|"member"|"quota"|""
	TargetID    string          `json:"targetId"`   // vmid / projectId / ""
	Outcome     string          `json:"outcome"`    // audit: pending|success|denied|error · task: running|succeeded|failed
	ProjectID   string          `json:"projectId"`
	ProjectName string          `json:"projectName"`
	UPID        string          `json:"upid,omitempty"`   // task rows only
	Detail      json.RawMessage `json:"detail,omitempty"` // audit detail passthrough
}

// ActivityPage is one keyset page of the activity timeline. NextBefore is passed
// back as ?before= to fetch the next (older) page; nil when no older audit rows
// remain.
type ActivityPage struct {
	Entries    []ActivityEntry `json:"entries"`
	NextBefore *time.Time      `json:"nextBefore"`
}
