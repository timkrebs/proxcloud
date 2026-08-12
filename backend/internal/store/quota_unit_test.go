package store

import (
	"errors"
	"testing"
)

func iptr(v int) *int       { return &v }
func i64ptr(v int64) *int64 { return &v }

// TestUsageOfRow covers the active/pending/other contribution rules (ADR-0012
// §1.3): an active row reads the snapshot (absent ⇒ not counted); a pending row
// reads reserved_* (always counted).
func TestUsageOfRow(t *testing.T) {
	snap := map[int]Alloc{101: {VCPU: 4, RAMMB: 2048, DiskGB: 40}}

	tests := []struct {
		name        string
		status      string
		vmid        int
		rv          *int
		rr, rd      *int64
		wantAlloc   Alloc
		wantCounted bool
	}{
		{"active present in snapshot", "active", 101, nil, nil, nil, Alloc{VCPU: 4, RAMMB: 2048, DiskGB: 40}, true},
		{"active absent from snapshot", "active", 999, nil, nil, nil, Alloc{}, false},
		{"pending reads reserved", "pending", 200, iptr(2), i64ptr(1024), i64ptr(20), Alloc{VCPU: 2, RAMMB: 1024, DiskGB: 20}, true},
		{"pending nil reserved counts zero-alloc", "pending", 201, nil, nil, nil, Alloc{}, true},
		{"tombstoned never counts", "tombstoned", 101, nil, nil, nil, Alloc{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotAlloc, gotCounted := usageOfRow(tt.status, tt.vmid, snap, tt.rv, tt.rr, tt.rd)
			if gotCounted != tt.wantCounted || gotAlloc != tt.wantAlloc {
				t.Fatalf("usageOfRow = (%+v, %v), want (%+v, %v)", gotAlloc, gotCounted, tt.wantAlloc, tt.wantCounted)
			}
		})
	}
}

// TestCheckQuota covers the per-dimension enforcement, the unlimited (nil) cases,
// and the count dimension (delta is always 1).
func TestCheckQuota(t *testing.T) {
	tests := []struct {
		name    string
		quota   *Quota
		usage   QuotaUsage
		delta   Alloc
		wantDim string // "" = no violation
	}{
		{"nil quota is unlimited", nil, QuotaUsage{VCPU: 999}, Alloc{VCPU: 999}, ""},
		{"vcpu within limit", &Quota{MaxVCPU: iptr(8)}, QuotaUsage{VCPU: 4}, Alloc{VCPU: 4}, ""},
		{"vcpu exceeds", &Quota{MaxVCPU: iptr(8)}, QuotaUsage{VCPU: 6}, Alloc{VCPU: 4}, "vcpu"},
		{"ram exceeds", &Quota{MaxRAMMB: i64ptr(4096)}, QuotaUsage{RAMMB: 3072}, Alloc{RAMMB: 2048}, "ram_mb"},
		{"disk exceeds", &Quota{MaxDiskGB: i64ptr(100)}, QuotaUsage{DiskGB: 80}, Alloc{DiskGB: 40}, "disk_gb"},
		{"count exceeds", &Quota{MaxCount: iptr(3)}, QuotaUsage{Count: 3}, Alloc{}, "count"},
		{"count at boundary ok", &Quota{MaxCount: iptr(3)}, QuotaUsage{Count: 2}, Alloc{}, ""},
		{"exact fit is allowed", &Quota{MaxVCPU: iptr(8)}, QuotaUsage{VCPU: 4}, Alloc{VCPU: 4}, ""},
		{"vcpu checked before ram", &Quota{MaxVCPU: iptr(1), MaxRAMMB: i64ptr(1)}, QuotaUsage{VCPU: 1, RAMMB: 1}, Alloc{VCPU: 1, RAMMB: 1}, "vcpu"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkQuota("project", tt.quota, tt.usage, tt.delta)
			if tt.wantDim == "" {
				if err != nil {
					t.Fatalf("checkQuota = %v, want nil", err)
				}
				return
			}
			var qe ErrQuotaExceeded
			if !errors.As(err, &qe) {
				t.Fatalf("checkQuota = %v, want ErrQuotaExceeded", err)
			}
			if qe.Dimension != tt.wantDim {
				t.Fatalf("dimension = %q, want %q", qe.Dimension, tt.wantDim)
			}
			if qe.Scope != "project" {
				t.Fatalf("scope = %q, want project", qe.Scope)
			}
		})
	}
}

// TestQuotaExceededReportsUsedLimitRequested checks the numbers the 409 message
// is rendered from (used before the create, the cap, the requested delta).
func TestQuotaExceededReportsUsedLimitRequested(t *testing.T) {
	err := checkQuota("project", &Quota{MaxVCPU: iptr(8)}, QuotaUsage{VCPU: 6}, Alloc{VCPU: 4})
	var qe ErrQuotaExceeded
	if !errors.As(err, &qe) {
		t.Fatalf("want ErrQuotaExceeded, got %v", err)
	}
	if qe.Used != 6 || qe.Limit != 8 || qe.Requested != 4 {
		t.Fatalf("got used=%d limit=%d requested=%d, want 6/8/4", qe.Used, qe.Limit, qe.Requested)
	}
}

// TestAdvisoryKeyTenant is deterministic and distinguishes tenants.
func TestAdvisoryKeyTenant(t *testing.T) {
	a1 := AdvisoryKeyTenant("tenant-a")
	a2 := AdvisoryKeyTenant("tenant-a")
	b := AdvisoryKeyTenant("tenant-b")
	if a1 != a2 {
		t.Fatalf("AdvisoryKeyTenant not deterministic: %d vs %d", a1, a2)
	}
	if a1 == b {
		t.Fatalf("AdvisoryKeyTenant collided for distinct tenants: %d", a1)
	}
}
