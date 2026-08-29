package events

import (
	"testing"

	types "github.com/timkrebs9/proxcloud/backend/api/types"
)

// TestDeliverTenantScoping locks down the per-connection SSE scope (ADR-0011),
// the Phase-3 acceptance rule: node-metrics frames are platform-admin only, and
// task/deployment frames reach a tenant subscriber only when the event's VMID is
// in that tenant's owned set. Platform-admin bypasses the VMID filter. The
// owned set is server-derived; a malformed/typed-mismatched payload must never
// leak (fail-closed to "do not deliver").
func TestDeliverTenantScoping(t *testing.T) {
	owned := map[int]bool{200: true} // tenant B owns VMID 200 only

	taskFor := func(vmid int) Event {
		return Event{Name: "task", Data: types.TaskEvent{
			UPID:     "UPID:pve01:1:2:3:qmstart:" + itoa(vmid) + ":root@pam:",
			Resource: &types.TaskResource{Type: "qemu", VMID: vmid, Node: "pve01"},
		}}
	}
	depFor := func(vmid int) Event {
		return Event{Name: "deployment", Data: &types.Deployment{VMID: vmid}}
	}
	setFor := func(vmids ...int) Event {
		ms := make([]types.DeploymentSetMember, 0, len(vmids))
		for _, v := range vmids {
			ms = append(ms, types.DeploymentSetMember{VMID: v})
		}
		return Event{Name: "deployment_set", Data: &types.DeploymentSet{ID: "set-1", Members: ms}}
	}

	tests := []struct {
		name  string
		e     Event
		admin bool
		want  bool
	}{
		{"metrics to admin", Event{Name: "metrics", Data: map[string]any{"cpu": 1}}, true, true},
		{"metrics to tenant user blocked", Event{Name: "metrics", Data: map[string]any{"cpu": 1}}, false, false},

		{"task admin bypass (foreign vmid)", taskFor(9012), true, true},
		{"task tenant owned vmid", taskFor(200), false, true},
		{"task tenant foreign vmid blocked", taskFor(100), false, false},
		{"task tenant nil resource blocked", Event{Name: "task", Data: types.TaskEvent{Resource: nil}}, false, false},
		{"task tenant wrong payload type blocked", Event{Name: "task", Data: "garbage"}, false, false},

		{"deployment admin bypass (foreign vmid)", depFor(9012), true, true},
		{"deployment tenant owned vmid", depFor(200), false, true},
		{"deployment tenant foreign vmid blocked", depFor(100), false, false},
		{"deployment tenant wrong payload type blocked", Event{Name: "deployment", Data: "garbage"}, false, false},

		{"schedule_warning admin bypass (foreign vmid)", Event{Name: "schedule_warning", Data: types.ScheduleWarningEvent{VMID: 9012}}, true, true},
		{"schedule_warning tenant owned vmid", Event{Name: "schedule_warning", Data: types.ScheduleWarningEvent{VMID: 200}}, false, true},
		{"schedule_warning tenant foreign vmid blocked", Event{Name: "schedule_warning", Data: types.ScheduleWarningEvent{VMID: 100}}, false, false},
		{"schedule_warning tenant wrong payload type blocked", Event{Name: "schedule_warning", Data: "garbage"}, false, false},

		{"ttl_warning admin bypass (foreign vmid)", Event{Name: "ttl_warning", Data: types.TtlWarningEvent{VMID: 9012}}, true, true},
		{"ttl_warning tenant owned vmid", Event{Name: "ttl_warning", Data: types.TtlWarningEvent{VMID: 200}}, false, true},
		{"ttl_warning tenant foreign vmid blocked", Event{Name: "ttl_warning", Data: types.TtlWarningEvent{VMID: 100}}, false, false},
		{"ttl_warning tenant wrong payload type blocked", Event{Name: "ttl_warning", Data: "garbage"}, false, false},

		// Deployment-set frames reach a tenant subscriber only when EVERY member VMID
		// is owned; platform-admin bypasses. A set naming any foreign VMID, an empty
		// member list, or a typed-mismatched payload must NOT fall through to the
		// default broadcast (which would leak the tenant's cluster topology).
		{"deployment_set admin bypass (foreign vmids)", setFor(9012, 9013), true, true},
		{"deployment_set tenant all owned", setFor(200), false, true},
		{"deployment_set tenant one foreign vmid blocked", setFor(200, 100), false, false},
		{"deployment_set tenant all foreign blocked", setFor(100, 101), false, false},
		{"deployment_set tenant empty members blocked", setFor(), false, false},
		{"deployment_set tenant wrong payload type blocked", Event{Name: "deployment_set", Data: "garbage"}, false, false},

		{"unknown frame passes through", Event{Name: "hello", Data: nil}, false, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// For admin cases the owned set is nil in production (admin bypasses);
			// pass nil to prove the code path never dereferences it.
			set := owned
			if tt.admin {
				set = nil
			}
			if got := deliver(tt.e, tt.admin, set); got != tt.want {
				t.Fatalf("deliver(%s) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

// itoa is a tiny local int->string to avoid importing strconv for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
