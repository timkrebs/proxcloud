package types

import "time"

// MetricPoint is one sample of one series. T serializes as RFC3339.
type MetricPoint struct {
	T time.Time `json:"t"`
	V float64   `json:"v"`
}

// MetricsResponse is a set of named time series for one timeframe
// (hour | day | week | month | year). Series keys are the PVE RRD field
// names (cpu, iowait, memused, memtotal, netin, netout, ...); cpu and
// iowait are percent 0-100, memory sizes raw bytes, net rates bytes/s.
// Samples PVE reports as null are omitted, never fabricated.
type MetricsResponse struct {
	Timeframe string                   `json:"timeframe"`
	Series    map[string][]MetricPoint `json:"series"`
}
