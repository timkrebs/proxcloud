//go:build integration

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// resetTTLTables clears the TTL tables between integration tests. Guarded against
// non-ephemeral databases (see guardDestructive).
func resetTTLTables(t *testing.T, s *PgStore) {
	t.Helper()
	guardDestructive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.pool.Exec(ctx, `DELETE FROM ttls`); err != nil {
		t.Fatalf("reset ttls: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM project_ttl_policy`); err != nil {
		t.Fatalf("reset project_ttl_policy: %v", err)
	}
}

// TestTTLLifecycle exercises the seconds<->Duration mapping and the warn-flag +
// extend bookkeeping end-to-end against Postgres (ADR-0020).
func TestTTLLifecycle(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetTTLTables(t, s)
	t.Cleanup(func() { resetTTLTables(t, s) })
	ctx := context.Background()
	tid, pid := defaultOwner(t, s) // seeded default tenant + project

	expiry := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	ttl, err := s.UpsertTTL(ctx, UpsertTTLParams{
		TenantID: *tid, ProjectID: *pid, VMID: 90101, Action: "delete",
		ExpiresAt: expiry, OriginalDuration: 48 * time.Hour,
	})
	if err != nil {
		t.Fatalf("UpsertTTL: %v", err)
	}
	if ttl.OriginalDuration != 48*time.Hour {
		t.Fatalf("original_duration round-trip = %v, want 48h", ttl.OriginalDuration)
	}
	if ttl.Warned24h || ttl.Warned1h {
		t.Fatalf("fresh TTL should be un-warned: %+v", ttl)
	}

	if err := s.SetTTLWarned(ctx, 90101, "24h"); err != nil {
		t.Fatalf("SetTTLWarned: %v", err)
	}
	got, err := s.GetTTL(ctx, 90101)
	if err != nil {
		t.Fatalf("GetTTL: %v", err)
	}
	if !got.Warned24h || got.Warned1h {
		t.Fatalf("after SetTTLWarned(24h): %+v", got)
	}

	// Extend resets both flags.
	newExpiry := expiry.Add(48 * time.Hour)
	if err := s.UpdateTTLExpiry(ctx, 90101, newExpiry); err != nil {
		t.Fatalf("UpdateTTLExpiry: %v", err)
	}
	got, _ = s.GetTTL(ctx, 90101)
	if got.Warned24h || got.Warned1h || !got.ExpiresAt.Equal(newExpiry) {
		t.Fatalf("after extend: %+v (want flags reset, expiry %v)", got, newExpiry)
	}

	// Re-upsert (ON CONFLICT (vmid)) also resets flags.
	if err := s.SetTTLWarned(ctx, 90101, "1h"); err != nil {
		t.Fatalf("SetTTLWarned(1h): %v", err)
	}
	if _, err := s.UpsertTTL(ctx, UpsertTTLParams{
		TenantID: *tid, ProjectID: *pid, VMID: 90101, Action: "stop",
		ExpiresAt: expiry, OriginalDuration: time.Hour,
	}); err != nil {
		t.Fatalf("re-UpsertTTL: %v", err)
	}
	got, _ = s.GetTTL(ctx, 90101)
	if got.Warned1h || got.Action != "stop" {
		t.Fatalf("re-upsert did not reset flags/action: %+v", got)
	}

	list, err := s.ListTTLsByProject(ctx, *pid)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListTTLsByProject = (%d, %v), want (1, nil)", len(list), err)
	}

	if err := s.DeleteTTL(ctx, 90101); err != nil {
		t.Fatalf("DeleteTTL: %v", err)
	}
	if _, err := s.GetTTL(ctx, 90101); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetTTL after delete = %v, want ErrNotFound", err)
	}
	if err := s.DeleteTTL(ctx, 90101); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteTTL (again) = %v, want ErrNotFound", err)
	}
}

// TestProjectTTLPolicyDefaultsAndUpsert covers the policy sidecar: absent → the
// caller-observed ErrNotFound (treated as default), and the nullable default_ttl.
func TestProjectTTLPolicyDefaultsAndUpsert(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetTTLTables(t, s)
	t.Cleanup(func() { resetTTLTables(t, s) })
	ctx := context.Background()
	tid, pid := defaultOwner(t, s)

	if _, err := s.GetProjectTTLPolicy(ctx, *tid, *pid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("absent policy = %v, want ErrNotFound", err)
	}

	def := 6 * time.Hour
	pol, err := s.UpsertProjectTTLPolicy(ctx, UpsertProjectTTLPolicyParams{
		TenantID: *tid, ProjectID: *pid, DefaultTTL: &def, MaxTTL: 7 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("UpsertProjectTTLPolicy: %v", err)
	}
	if pol.DefaultTTL == nil || *pol.DefaultTTL != def || pol.MaxTTL != 7*24*time.Hour {
		t.Fatalf("policy round-trip wrong: %+v", pol)
	}

	// Clear the default (nil) on re-upsert.
	pol, err = s.UpsertProjectTTLPolicy(ctx, UpsertProjectTTLPolicyParams{
		TenantID: *tid, ProjectID: *pid, DefaultTTL: nil, MaxTTL: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("re-Upsert: %v", err)
	}
	if pol.DefaultTTL != nil || pol.MaxTTL != 30*24*time.Hour {
		t.Fatalf("cleared-default policy wrong: %+v", pol)
	}
}
