package lifecycle

import (
	"reflect"
	"testing"
)

func TestParseHHMM(t *testing.T) {
	tests := []struct {
		in      string
		hh, mm  int
		wantErr bool
	}{
		{"21:45", 21, 45, false},
		{"00:00", 0, 0, false},
		{"23:59", 23, 59, false},
		{"7:5", 7, 5, false},
		{"24:00", 0, 0, true},
		{"12:60", 0, 0, true},
		{"12", 0, 0, true},
		{"aa:bb", 0, 0, true},
		{"", 0, 0, true},
	}
	for _, tt := range tests {
		hh, mm, err := parseHHMM(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseHHMM(%q) err = %v, wantErr %v", tt.in, err, tt.wantErr)
			continue
		}
		if err == nil && (hh != tt.hh || mm != tt.mm) {
			t.Errorf("parseHHMM(%q) = %d:%d, want %d:%d", tt.in, hh, mm, tt.hh, tt.mm)
		}
	}
}

func TestCronExpr(t *testing.T) {
	if got := cronExpr(21, 45, []int{1, 2, 3, 4, 5}); got != "45 21 * * 1,2,3,4,5" {
		t.Errorf("cronExpr = %q, want %q", got, "45 21 * * 1,2,3,4,5")
	}
	if got := cronExpr(7, 0, []int{0, 6}); got != "0 7 * * 0,6" {
		t.Errorf("cronExpr = %q, want %q", got, "0 7 * * 0,6")
	}
}

// TestWarnTime covers the T-15m computation including the midnight-crossing
// day-shift edge case (ADR-0019): a shutdown before 00:15 warns the previous
// evening, so the fire days shift one day earlier.
func TestWarnTime(t *testing.T) {
	tests := []struct {
		name     string
		hh, mm   int
		days     []int
		wantH    int
		wantM    int
		wantDays []int
	}{
		{"same day 21:45 -> 21:30", 21, 45, []int{1, 2, 3, 4, 5}, 21, 30, []int{1, 2, 3, 4, 5}},
		{"same day 00:20 -> 00:05", 0, 20, []int{3}, 0, 5, []int{3}},
		{"midnight cross 00:05 -> 23:50 prev day", 0, 5, []int{1}, 23, 50, []int{0}},
		{"midnight cross 00:00 sunday -> 23:45 saturday", 0, 0, []int{0}, 23, 45, []int{6}},
		{"midnight cross 00:10 multi", 0, 10, []int{1, 0}, 23, 55, []int{0, 6}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, m, days := warnTime(tt.hh, tt.mm, tt.days)
			if h != tt.wantH || m != tt.wantM {
				t.Errorf("warnTime time = %d:%d, want %d:%d", h, m, tt.wantH, tt.wantM)
			}
			if !reflect.DeepEqual(days, tt.wantDays) {
				t.Errorf("warnTime days = %v, want %v", days, tt.wantDays)
			}
		})
	}
}
