package types

import "time"

// TaskRef is the 202 response of every async mutation: the real Proxmox
// task UPID plus the friendly action label the UI shows immediately.
type TaskRef struct {
	UPID   string `json:"upid"`
	Action string `json:"action"`
}

// TaskResource points a task at the guest it operates on, when known.
type TaskResource struct {
	Type string `json:"type"` // qemu | lxc
	VMID int    `json:"vmid"`
	Node string `json:"node"`
	Name string `json:"name,omitempty"`
}

// TaskSummary is one row of the activity log: a Proxmox task with
// normalized status (running | succeeded | failed) and a human action label.
type TaskSummary struct {
	UPID       string        `json:"upid"`
	Node       string        `json:"node"`
	Type       string        `json:"type"`   // raw PVE task type, e.g. qmstart
	Action     string        `json:"action"` // friendly label, e.g. "Start virtual machine"
	User       string        `json:"user"`
	Resource   *TaskResource `json:"resource,omitempty"`
	Status     string        `json:"status"` // running | succeeded | failed
	ExitStatus string        `json:"exitStatus,omitempty"`
	StartedAt  time.Time     `json:"startedAt"`
	EndedAt    *time.Time    `json:"endedAt,omitempty"`
}

// TaskLogLine is one line of a task's log.
type TaskLogLine struct {
	N int    `json:"n"`
	T string `json:"t"`
}

// TaskLog is a paginated slice of a task's log.
type TaskLog struct {
	Total int           `json:"total"`
	Lines []TaskLogLine `json:"lines"`
}
