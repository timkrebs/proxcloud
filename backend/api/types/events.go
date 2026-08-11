package types

import "time"

// NodeMetric is one node's live sample inside a MetricsEvent. Zeroes with
// Online=false mean "could not fetch", never a fabricated measurement.
type NodeMetric struct {
	Node      string  `json:"node"`
	Online    bool    `json:"online"`
	CPUPct    float64 `json:"cpuPct"`
	MemUsed   int64   `json:"memUsed"`
	MemTotal  int64   `json:"memTotal"`
	UptimeSec int64   `json:"uptimeSec"`
}

// MetricsEvent is the SSE "metrics" payload, emitted every poll tick.
type MetricsEvent struct {
	TS    time.Time    `json:"ts"`
	Nodes []NodeMetric `json:"nodes"`
}

// TaskEvent is the SSE "task" payload, emitted when a tracked task starts
// or finishes.
type TaskEvent struct {
	UPID       string        `json:"upid"`
	Action     string        `json:"action"`
	Status     string        `json:"status"` // running | succeeded | failed
	ExitStatus string        `json:"exitStatus,omitempty"`
	Resource   *TaskResource `json:"resource,omitempty"`
}
