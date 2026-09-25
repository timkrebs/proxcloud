package handlers

import "testing"

// TestNamedDiskSizeGiB is the M-a.3 unit table: the per-disk size parser reads
// the NAMED key's own size= attribute (floored to whole GiB — flooring can only
// over-state a growth delta, never under-state it) and rejects, with explicit
// errors, everything it cannot measure honestly.
func TestNamedDiskSizeGiB(t *testing.T) {
	cfg := map[string]any{
		"scsi0":   "local-lvm:vm-101-disk-0,size=32G",
		"scsi1":   "local-lvm:vm-101-disk-1,size=10G,ssd=1",
		"virtio2": "local-lvm:vm-101-disk-2,size=4400M", // 4.29 GiB → floor 4
		"sata3":   "local-lvm:vm-101-disk-3,size=1T",
		"rootfs":  "local-lvm:vm-101-disk-0,size=8G",
		"mp0":     "local-lvm:vm-101-disk-4,mp=/data,size=100G",
		"ide2":    "local:iso/debian.iso,media=cdrom",
		"scsi9":   "local-lvm:vm-101-disk-9", // no size attribute
	}
	tests := []struct {
		key     string
		want    int64
		wantErr bool
	}{
		{"scsi0", 32, false},
		{"scsi1", 10, false},
		{"virtio2", 4, false}, // sub-GiB floored, conservative for delta math
		{"sata3", 1024, false},
		{"rootfs", 8, false},
		{"mp0", 100, false},
		{"ide2", 0, true},   // CD-ROM: not resizable
		{"scsi9", 0, true},  // size unavailable: explicit error, never invented
		{"scsi7", 0, true},  // key absent
		{"absent", 0, true}, // key absent
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, apiErr := namedDiskSizeGiB(cfg, tt.key)
			if (apiErr != nil) != tt.wantErr {
				t.Fatalf("namedDiskSizeGiB(%q) err = %v, wantErr %v", tt.key, apiErr, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("namedDiskSizeGiB(%q) = %d, want %d", tt.key, got, tt.want)
			}
		})
	}
}

// TestPVEMemoryMB covers both memory shapes PVE emits (plain MiB integer and
// the newer current= property form); unparseable input is 0 — unknown, never
// invented (the rollback gate then logs and skips that dimension).
func TestPVEMemoryMB(t *testing.T) {
	tests := []struct {
		in   string
		want int64
	}{
		{"2048", 2048},
		{" 8192 ", 8192},
		{"current=4096", 4096},
		{"current=4096,shares=1000", 4096},
		{"", 0},
		{"lots", 0},
	}
	for _, tt := range tests {
		if got := pveMemoryMB(tt.in); got != tt.want {
			t.Errorf("pveMemoryMB(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
