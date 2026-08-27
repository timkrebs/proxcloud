//go:build integration

package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// resetScheduleTables clears the schedule + ownership rows the schedule/auto_stopped
// integration tests touch. Guarded against non-ephemeral databases.
func resetScheduleTables(t *testing.T, s *PgStore) {
	t.Helper()
	guardDestructive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.pool.Exec(ctx, `DELETE FROM schedules`); err != nil {
		t.Fatalf("reset schedules: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM resource_ownership`); err != nil {
		t.Fatalf("reset resource_ownership: %v", err)
	}
}

func defaultIDs(t *testing.T, s *PgStore) (tenantID, projectID string) {
	t.Helper()
	ctx := context.Background()
	ten, err := s.GetTenantBySlug(ctx, "default")
	if err != nil {
		t.Fatalf("default tenant: %v", err)
	}
	proj, err := s.GetProjectByPoolID(ctx, "pc-default-default")
	if err != nil {
		t.Fatalf("default project: %v", err)
	}
	return ten.ID, proj.ID
}

func TestResourceScheduleUpsertConflict(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetScheduleTables(t, s)
	t.Cleanup(func() { resetScheduleTables(t, s) })
	ctx := context.Background()
	tid, pid := defaultIDs(t, s)

	start := "07:00"
	first, err := s.UpsertResourceSchedule(ctx, UpsertResourceScheduleParams{
		TenantID: tid, ProjectID: pid, VMID: 101,
		ShutdownTime: "21:45", AutoStartTime: &start, DaysOfWeek: []int{1, 2, 3, 4, 5},
		Timezone: "Europe/Berlin", GraceSeconds: 90, Enabled: true,
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if first.Scope != "resource" || first.VMID == nil || *first.VMID != 101 {
		t.Fatalf("first row wrong: %+v", first)
	}
	if !reflect.DeepEqual(first.DaysOfWeek, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("days_of_week round-trip = %v, want [1 2 3 4 5]", first.DaysOfWeek)
	}

	// Second upsert on the same (tenant, vmid) updates in place (same id).
	second, err := s.UpsertResourceSchedule(ctx, UpsertResourceScheduleParams{
		TenantID: tid, ProjectID: pid, VMID: 101,
		ShutdownTime: "19:00", DaysOfWeek: []int{0, 6}, Timezone: "UTC", GraceSeconds: 30, Enabled: false, OptOut: true,
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("conflict upsert created a new row (%s != %s)", second.ID, first.ID)
	}
	if second.ShutdownTime != "19:00" || second.Enabled || !second.OptOut || second.AutoStartTime != nil {
		t.Fatalf("conflict upsert did not replace fields: %+v", second)
	}

	got, err := s.GetResourceSchedule(ctx, 101)
	if err != nil {
		t.Fatalf("GetResourceSchedule: %v", err)
	}
	if got.ID != first.ID || got.ShutdownTime != "19:00" {
		t.Fatalf("get returned wrong row: %+v", got)
	}

	if err := s.DeleteResourceSchedule(ctx, 101); err != nil {
		t.Fatalf("DeleteResourceSchedule: %v", err)
	}
	if _, err := s.GetResourceSchedule(ctx, 101); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteResourceSchedule(ctx, 101); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}

func TestProjectScheduleUpsertGetDelete(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetScheduleTables(t, s)
	t.Cleanup(func() { resetScheduleTables(t, s) })
	ctx := context.Background()
	tid, pid := defaultIDs(t, s)

	first, err := s.UpsertProjectSchedule(ctx, UpsertProjectScheduleParams{
		TenantID: tid, ProjectID: pid, ShutdownTime: "22:00", DaysOfWeek: []int{1}, Timezone: "UTC", GraceSeconds: 120, Enabled: true,
	})
	if err != nil {
		t.Fatalf("upsert project schedule: %v", err)
	}
	if first.Scope != "project" || first.VMID != nil {
		t.Fatalf("project row wrong: %+v", first)
	}
	second, err := s.UpsertProjectSchedule(ctx, UpsertProjectScheduleParams{
		TenantID: tid, ProjectID: pid, ShutdownTime: "23:30", DaysOfWeek: []int{2, 3}, Timezone: "UTC", GraceSeconds: 60, Enabled: true,
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if second.ID != first.ID || second.ShutdownTime != "23:30" {
		t.Fatalf("project conflict upsert wrong: %+v", second)
	}

	got, err := s.GetProjectSchedule(ctx, tid, pid)
	if err != nil {
		t.Fatalf("GetProjectSchedule: %v", err)
	}
	if got.ID != first.ID {
		t.Fatalf("get returned wrong row: %+v", got)
	}

	list, err := s.ListSchedulesByProject(ctx, pid)
	if err != nil {
		t.Fatalf("ListSchedulesByProject: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("list by project = %d, want 1", len(list))
	}

	if err := s.DeleteProjectSchedule(ctx, tid, pid); err != nil {
		t.Fatalf("DeleteProjectSchedule: %v", err)
	}
	if _, err := s.GetProjectSchedule(ctx, tid, pid); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get after delete err = %v, want ErrNotFound", err)
	}
}

func TestSetAutoStopped(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetScheduleTables(t, s)
	t.Cleanup(func() { resetScheduleTables(t, s) })
	ctx := context.Background()
	tid, pid := defaultIDs(t, s)

	if _, err := s.CreateOwnership(ctx, CreateOwnershipParams{
		TenantID: tid, ProjectID: pid, VMID: 101, GuestType: "qemu", Node: "pve01", Status: "active",
	}); err != nil {
		t.Fatalf("CreateOwnership: %v", err)
	}
	own, err := s.GetOwnershipByVMID(ctx, 101)
	if err != nil {
		t.Fatalf("GetOwnershipByVMID: %v", err)
	}
	if own.AutoStopped {
		t.Fatal("new ownership row has auto_stopped=true, want default false")
	}

	if err := s.SetAutoStopped(ctx, 101, true); err != nil {
		t.Fatalf("SetAutoStopped(true): %v", err)
	}
	own, _ = s.GetOwnershipByVMID(ctx, 101)
	if !own.AutoStopped {
		t.Fatal("auto_stopped not set true")
	}

	if err := s.SetAutoStopped(ctx, 101, false); err != nil {
		t.Fatalf("SetAutoStopped(false): %v", err)
	}
	own, _ = s.GetOwnershipByVMID(ctx, 101)
	if own.AutoStopped {
		t.Fatal("auto_stopped not cleared")
	}

	// Unknown VMID → ErrNotFound.
	if err := s.SetAutoStopped(ctx, 999999, true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetAutoStopped(unknown) err = %v, want ErrNotFound", err)
	}
}
