package handlers

import (
	"errors"
	"testing"
)

// TestRollbackGrowthTarget pins the fail-closed sizing reader behind the
// rollback quota gate: absent keys take PVE's defaults, a VM's sockets
// multiply its cores, and anything present but unreadable is an error rather
// than "unchanged". A container without cores has no CPU limit at all.
func TestRollbackGrowthTarget(t *testing.T) {
	tests := []struct {
		name      string
		typ       string
		cfg       map[string]any
		wantVCPU  int
		wantRAMMB int64
		wantErr   bool
		unlimited bool
	}{
		{"vm plain", "qemu", map[string]any{"cores": float64(4), "memory": float64(4096)}, 4, 4096, false, false},
		{"vm sockets multiply", "qemu", map[string]any{"cores": float64(4), "sockets": float64(2), "memory": "current=8192"}, 8, 8192, false, false},
		{"vm all defaults", "qemu", map[string]any{}, 1, 512, false, false},
		{"vm cores unreadable", "qemu", map[string]any{"cores": "lots", "memory": float64(1024)}, 0, 0, true, false},
		{"vm fractional cores", "qemu", map[string]any{"cores": 2.5, "memory": float64(1024)}, 0, 0, true, false},
		{"vm zero sockets", "qemu", map[string]any{"cores": float64(2), "sockets": float64(0)}, 0, 0, true, false},
		{"vm memory unreadable", "qemu", map[string]any{"cores": float64(2), "memory": "huge"}, 0, 0, true, false},
		{"container plain", "lxc", map[string]any{"cores": float64(2), "memory": float64(1024)}, 2, 1024, false, false},
		{"container ignores sockets", "lxc", map[string]any{"cores": float64(2), "sockets": float64(4), "memory": float64(1024)}, 2, 1024, false, false},
		{"container memory default", "lxc", map[string]any{"cores": float64(1)}, 1, 512, false, false},
		{"container without cores is unlimited", "lxc", map[string]any{"memory": float64(512)}, 0, 0, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vcpu, ram, err := rollbackGrowthTarget(tt.typ, tt.cfg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("got %d vCPU / %d MiB, want an error", vcpu, ram)
				}
				if errors.Is(err, errUnlimitedCores) != tt.unlimited {
					t.Fatalf("err = %v, unlimited-cores = %v, want %v", err, errors.Is(err, errUnlimitedCores), tt.unlimited)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if vcpu != tt.wantVCPU || ram != tt.wantRAMMB {
				t.Fatalf("got %d vCPU / %d MiB, want %d / %d", vcpu, ram, tt.wantVCPU, tt.wantRAMMB)
			}
		})
	}
}

// TestGuestVCPUTarget: a config update's vCPU target is sockets × the requested
// cores for a VM (sockets defaulting to PVE's 1), the cores alone for a
// container, and an error for a malformed sockets value.
func TestGuestVCPUTarget(t *testing.T) {
	tests := []struct {
		name    string
		typ     string
		cfg     map[string]any
		cores   int
		want    int
		wantErr bool
	}{
		{"vm no config", "qemu", nil, 4, 4, false},
		{"vm three sockets", "qemu", map[string]any{"sockets": float64(3)}, 2, 6, false},
		{"vm sockets as string", "qemu", map[string]any{"sockets": "2"}, 4, 8, false},
		{"vm malformed sockets", "qemu", map[string]any{"sockets": "two"}, 2, 0, true},
		{"container ignores sockets", "lxc", map[string]any{"sockets": float64(3)}, 2, 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := guestVCPUTarget(tt.typ, tt.cfg, tt.cores)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("vCPU = %d, want %d", got, tt.want)
			}
		})
	}
}
