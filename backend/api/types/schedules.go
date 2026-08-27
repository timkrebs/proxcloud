package types

import "time"

// Schedule is the wire representation of an auto-shutdown schedule (ADR-0019).
// VMID is set only for resource scope. Times are "HH:MM" 24h, local to Timezone;
// DaysOfWeek are 0..6 (Sun..Sat). Every value comes from the stored row — the UI
// never sees or types cron.
type Schedule struct {
	ID            string    `json:"id"`
	Scope         string    `json:"scope"` // "resource" | "project"
	TenantID      string    `json:"tenantId"`
	ProjectID     string    `json:"projectId"`
	VMID          *int      `json:"vmid,omitempty"`
	ShutdownTime  string    `json:"shutdownTime"`            // "HH:MM"
	AutoStartTime *string   `json:"autoStartTime,omitempty"` // "HH:MM"; null = no auto-start
	DaysOfWeek    []int     `json:"daysOfWeek"`              // 0..6 (Sun..Sat)
	Timezone      string    `json:"timezone"`                // IANA name
	GraceSeconds  int       `json:"graceSeconds"`
	Enabled       bool      `json:"enabled"`
	OptOut        bool      `json:"optOut"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

// ScheduleRequest is the PUT body for a resource or project schedule. The backend
// validates every field (HH:MM format, days 0..6 non-empty, timezone against the
// tz database, grace > 0) and derives the cron internally — cron is never
// user-entered. OptOut is honored on resource scope only.
type ScheduleRequest struct {
	ShutdownTime  string  `json:"shutdownTime"`
	AutoStartTime *string `json:"autoStartTime,omitempty"`
	DaysOfWeek    []int   `json:"daysOfWeek"`
	Timezone      string  `json:"timezone"`
	GraceSeconds  int     `json:"graceSeconds"`
	Enabled       bool    `json:"enabled"`
	OptOut        bool    `json:"optOut,omitempty"`
}

// ScheduleSkipResult reports the outcome of POST …/schedule/skip: how many of the
// guest's next auto-shutdown job occurrences were advanced one boundary, and the
// new next-run of the stop job (nil when there is no active stop job to skip).
type ScheduleSkipResult struct {
	Skipped   int        `json:"skipped"`
	NextRunAt *time.Time `json:"nextRunAt,omitempty"`
}

// ScheduleWarningEvent is the SSE "schedule_warning" payload: an advance heads-up
// (T-15m) that a guest is about to be auto-shut-down. It carries the owning VMID
// so events.deliver can scope it to the owning tenant (it is NOT broadcast).
type ScheduleWarningEvent struct {
	VMID        int       `json:"vmid"`
	Node        string    `json:"node"`
	GuestType   string    `json:"guestType"` // qemu | lxc
	Kind        string    `json:"kind"`      // "autoshutdown"
	Title       string    `json:"title"`
	Detail      string    `json:"detail"`
	ScheduledAt time.Time `json:"scheduledAt"` // when the shutdown fires
}
