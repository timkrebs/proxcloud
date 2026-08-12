package auditz_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/timkrebs9/proxcloud/backend/internal/auditz"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

func newRecorder(t *testing.T) (*auditz.Recorder, *storetest.Fake, *bytes.Buffer) {
	t.Helper()
	fake := storetest.New()
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelError}))
	return &auditz.Recorder{Store: fake, Log: log}, fake, &buf
}

// Begin writes a durable intent row (outcome "pending"), carrying actor/action.
func TestBeginInsertsPendingIntent(t *testing.T) {
	rec, fake, _ := newRecorder(t)

	p, err := rec.Begin(context.Background(), auditz.Intent{
		Action:      "tenant.create",
		ActorUserID: "user-1",
		TargetType:  "tenant",
	})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if p.ID() == "" {
		t.Fatal("Begin returned an empty intent id")
	}
	rows := fake.AllAudit()
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	e := rows[0]
	if e.Action != "tenant.create" || e.Outcome != "pending" {
		t.Fatalf("intent row = action %q outcome %q, want tenant.create/pending", e.Action, e.Outcome)
	}
	if e.ActorUserID == nil || *e.ActorUserID != "user-1" {
		t.Fatalf("actor = %v, want user-1", e.ActorUserID)
	}
	if e.TargetType == nil || *e.TargetType != "tenant" {
		t.Fatalf("target_type = %v, want tenant", e.TargetType)
	}
	// Empty-string fields map to NULL columns (no fabricated tenant/target id).
	if e.TenantID != nil || e.TargetID != nil {
		t.Fatalf("tenant_id/target_id = %v/%v, want nil (unknown at intent time)", e.TenantID, e.TargetID)
	}
}

// A failed InsertAuditIntent propagates from Begin so the caller can fail-closed —
// and leaves no row.
func TestBeginPropagatesInsertError(t *testing.T) {
	rec, fake, _ := newRecorder(t)
	fake.FailOn("InsertAuditIntent", errors.New("audit db down"))

	if _, err := rec.Begin(context.Background(), auditz.Intent{Action: "tenant.create"}); err == nil {
		t.Fatal("Begin returned nil error, want the store failure propagated")
	}
	if rows := fake.AllAudit(); len(rows) != 0 {
		t.Fatalf("audit rows = %d, want 0 (intent insert failed)", len(rows))
	}
}

// Finalize writes the one-way outcome + jsonb detail on the same row.
func TestFinalizeSetsOutcomeAndDetail(t *testing.T) {
	rec, fake, _ := newRecorder(t)
	p, err := rec.Begin(context.Background(), auditz.Intent{Action: "tenant.quota.update", ActorUserID: "root", TenantID: "ten-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	p.Finalize(context.Background(), "success", map[string]any{"status": 200})

	rows, err := fake.ListAudit(context.Background(), store.AuditQuery{TenantID: "ten-1", Limit: 10})
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("audit rows = %d, want 1", len(rows))
	}
	if rows[0].Outcome != "success" {
		t.Fatalf("outcome = %q, want success", rows[0].Outcome)
	}
	if !bytes.Contains(rows[0].Detail, []byte(`"status":200`)) {
		t.Fatalf("detail = %s, want status 200", rows[0].Detail)
	}
}

// A Finalize failure is logged, not surfaced (no panic/return); the durable
// intent row stays "pending".
func TestFinalizeSwallowsAndLogsError(t *testing.T) {
	rec, fake, buf := newRecorder(t)
	p, err := rec.Begin(context.Background(), auditz.Intent{Action: "tenant.create", TenantID: "ten-1"})
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}
	fake.FailOn("FinalizeAudit", errors.New("audit db blip"))
	p.Finalize(context.Background(), "success", map[string]any{"status": 201}) // must not panic

	if rows := fake.AllAudit(); len(rows) != 1 || rows[0].Outcome != "pending" {
		t.Fatalf("row after failed finalize = %+v, want a single still-pending row", rows)
	}
	if !strings.Contains(buf.String(), "finalize failed") {
		t.Fatalf("expected a loud finalize-failure log, got:\n%s", buf.String())
	}
}

func TestOutcomeForStatus(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{0, "success"}, {200, "success"}, {201, "success"}, {204, "success"},
		{400, "denied"}, {403, "denied"}, {404, "denied"}, {409, "denied"},
		{500, "error"}, {502, "error"},
	}
	for _, c := range cases {
		if got := auditz.OutcomeForStatus(c.status); got != c.want {
			t.Errorf("OutcomeForStatus(%d) = %q, want %q", c.status, got, c.want)
		}
	}
}
