//go:build integration

package store

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source/iofs"

	"github.com/timkrebs9/proxcloud/backend/migrations"
)

// newMigrate builds a golang-migrate instance against the test DB, mirroring
// PgStore.RunMigrations' wiring but exposing Up/Steps for down-migration testing.
func newMigrate(t *testing.T, s *PgStore) (*migrate.Migrate, *sql.DB) {
	t.Helper()
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatalf("migration source: %v", err)
	}
	db, err := sql.Open("pgx", s.dsn)
	if err != nil {
		t.Fatalf("open migrate db: %v", err)
	}
	driver, err := postgres.WithInstance(db, &postgres.Config{})
	if err != nil {
		t.Fatalf("migrate driver: %v", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "postgres", driver)
	if err != nil {
		t.Fatalf("migrate init: %v", err)
	}
	return m, db
}

func tableExists(t *testing.T, s *PgStore, name string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables
		 WHERE table_schema='public' AND table_name=$1)`, name).Scan(&exists); err != nil {
		t.Fatalf("table exists %q: %v", name, err)
	}
	return exists
}

func columnExists(t *testing.T, s *PgStore, table, col string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var exists bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.columns
		 WHERE table_schema='public' AND table_name=$1 AND column_name=$2)`, table, col).Scan(&exists); err != nil {
		t.Fatalf("column exists %q.%q: %v", table, col, err)
	}
	return exists
}

// TestSchedulerWaveMigrationsReversible proves migrations 000005/000006/000007/000008
// apply cleanly, reverse cleanly (Steps(-4) → version 4), and re-apply (up →
// down → up) landing back on version 8 with the full object set. It restores the
// DB to the fully-migrated state on exit so it stays green for sibling tests.
func TestSchedulerWaveMigrationsReversible(t *testing.T) {
	s := requireStore(t)

	m, db := newMigrate(t, s)
	defer db.Close()

	// Always leave the shared test DB fully migrated, even on a mid-test failure.
	t.Cleanup(func() {
		if err := m.Up(); err != nil && err != migrate.ErrNoChange {
			t.Errorf("cleanup migrate up: %v", err)
		}
	})

	// Up: full schema, version 8.
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up: %v", err)
	}
	if v, _, err := m.Version(); err != nil || v != 8 {
		t.Fatalf("version after up = %d (err %v), want 8", v, err)
	}

	// The four wave migrations' objects exist (deployment_set from 000008).
	waveTables := []string{"jobs", "schedules", "ttls", "project_ttl_policy", "deployment_set"}
	for _, tbl := range waveTables {
		if !tableExists(t, s, tbl) {
			t.Fatalf("table %q missing after up", tbl)
		}
	}
	if !columnExists(t, s, "audit_log", "actor_system") {
		t.Fatal("audit_log.actor_system missing after up")
	}
	if !columnExists(t, s, "resource_ownership", "auto_stopped") {
		t.Fatal("resource_ownership.auto_stopped missing after up")
	}
	if !columnExists(t, s, "resource_ownership", "expired_at") {
		t.Fatal("resource_ownership.expired_at missing after up")
	}

	// Down: reverse exactly the four wave migrations (8→7→6→5→4).
	if err := m.Steps(-4); err != nil {
		t.Fatalf("migrate down 4 steps: %v", err)
	}
	if v, dirty, err := m.Version(); err != nil || v != 4 || dirty {
		t.Fatalf("version after down = %d dirty=%v (err %v), want 4 clean", v, dirty, err)
	}
	for _, tbl := range waveTables {
		if tableExists(t, s, tbl) {
			t.Fatalf("table %q survived down migration", tbl)
		}
	}
	if columnExists(t, s, "audit_log", "actor_system") {
		t.Fatal("audit_log.actor_system survived down migration")
	}
	if columnExists(t, s, "resource_ownership", "auto_stopped") {
		t.Fatal("resource_ownership.auto_stopped survived down migration")
	}
	if columnExists(t, s, "resource_ownership", "expired_at") {
		t.Fatal("resource_ownership.expired_at survived down migration")
	}

	// Up again: back to 8 with every wave object restored (reversibility).
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate re-up: %v", err)
	}
	if v, dirty, err := m.Version(); err != nil || v != 8 || dirty {
		t.Fatalf("version after re-up = %d dirty=%v (err %v), want 8 clean", v, dirty, err)
	}
	for _, tbl := range waveTables {
		if !tableExists(t, s, tbl) {
			t.Fatalf("table %q not restored after re-up", tbl)
		}
	}
}
