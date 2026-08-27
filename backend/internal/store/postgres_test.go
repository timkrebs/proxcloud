package store

import (
	"context"
	"os"
	"sort"
	"testing"
	"time"
)

// requireStore connects to the Postgres named by DATABASE_URL and runs
// migrations. Without DATABASE_URL the test skips cleanly, so `go test ./...`
// stays green in CI without a database.
func requireStore(t *testing.T) *PgStore {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set — skipping Postgres integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	s, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestRunMigrationsCreatesSchemaAndIsIdempotent(t *testing.T) {
	s := requireStore(t)

	v1, err := s.RunMigrations()
	if err != nil {
		t.Fatalf("RunMigrations (first): %v", err)
	}
	if v1 != 5 {
		t.Fatalf("expected schema version 5, got %d", v1)
	}

	// Idempotency: a second run is a no-op landing on the same version.
	v2, err := s.RunMigrations()
	if err != nil {
		t.Fatalf("RunMigrations (second): %v", err)
	}
	if v2 != v1 {
		t.Fatalf("migrations not idempotent: version %d then %d", v1, v2)
	}

	// Every table from the data model must exist.
	want := []string{
		"audit_log", "invitations", "jobs", "login_challenges", "memberships", "projects",
		"quotas", "recovery_codes", "resource_ownership", "sessions", "tenants",
		"totp_secrets", "users",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, err := s.pool.Query(ctx,
		`SELECT table_name FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_type = 'BASE TABLE'
		   AND table_name <> 'schema_migrations'
		 ORDER BY table_name`)
	if err != nil {
		t.Fatalf("query tables: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("table set mismatch:\n got=%v\nwant=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("table set mismatch:\n got=%v\nwant=%v", got, want)
		}
	}
}

func TestSeededDefaultTenantAndProject(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ten, err := s.GetTenantBySlug(ctx, "default")
	if err != nil {
		t.Fatalf("GetTenantBySlug(default): %v", err)
	}
	if ten.Name != "Default" {
		t.Fatalf("default tenant name = %q, want Default", ten.Name)
	}

	proj, err := s.GetProjectByPoolID(ctx, "pc-default-default")
	if err != nil {
		t.Fatalf("GetProjectByPoolID(pc-default-default): %v", err)
	}
	if proj.TenantID != ten.ID {
		t.Fatalf("default project tenant_id = %q, want %q", proj.TenantID, ten.ID)
	}
	if proj.Slug != "default" {
		t.Fatalf("default project slug = %q, want default", proj.Slug)
	}

	projs, err := s.ListProjectsByTenant(ctx, ten.ID)
	if err != nil {
		t.Fatalf("ListProjectsByTenant: %v", err)
	}
	if len(projs) != 1 || projs[0].PoolID != "pc-default-default" {
		t.Fatalf("ListProjectsByTenant = %+v, want single pc-default-default", projs)
	}

	if _, err := s.GetTenantBySlug(ctx, "does-not-exist"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound for missing tenant, got %v", err)
	}
}
