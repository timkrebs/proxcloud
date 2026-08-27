//go:build integration

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// resetPhase3Tables clears the tenancy aggregates each Phase-3 integration test
// touches (ownership, non-default projects, non-default tenants, users). It
// preserves the migration-seeded default tenant + default project so tests that
// depend on them (TestSeededDefaultTenantAndProject) still pass regardless of
// order. Guarded against non-ephemeral databases (see guardDestructive).
func resetPhase3Tables(t *testing.T, s *PgStore) {
	t.Helper()
	guardDestructive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Order matters: resource_ownership.created_by references users with NO
	// ACTION, so ownership must be cleared before users; projects reference
	// tenants ON DELETE CASCADE. Keep the seeded default tenant/project.
	for _, q := range []string{
		`DELETE FROM resource_ownership`,
		`DELETE FROM projects WHERE pool_id <> 'pc-default-default'`,
		`DELETE FROM tenants WHERE slug <> 'default'`,
		`DELETE FROM users`,
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatalf("reset phase3 (%s): %v", q, err)
		}
	}
}

func TestTenantAndProjectLifecycle(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetPhase3Tables(t, s)
	t.Cleanup(func() { resetPhase3Tables(t, s) })
	ctx := context.Background()

	ten, err := s.CreateTenant(ctx, CreateTenantParams{Name: "Acme", Slug: "acme"})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if ten.ID == "" || ten.Name != "Acme" || ten.Slug != "acme" {
		t.Fatalf("CreateTenant returned %+v", ten)
	}

	got, err := s.GetTenantByID(ctx, ten.ID)
	if err != nil || got.ID != ten.ID {
		t.Fatalf("GetTenantByID = (%+v,%v)", got, err)
	}
	if _, err := s.GetTenantByID(ctx, "00000000-0000-0000-0000-000000000000"); err != ErrNotFound {
		t.Fatalf("GetTenantByID(missing) = %v, want ErrNotFound", err)
	}

	proj, err := s.CreateProject(ctx, CreateProjectParams{
		TenantID: ten.ID, Name: "Web", Slug: "web", PoolID: "pc-acme-web",
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if proj.TenantID != ten.ID || proj.PoolID != "pc-acme-web" {
		t.Fatalf("CreateProject returned %+v", proj)
	}

	gotProj, err := s.GetProjectByID(ctx, proj.ID)
	if err != nil || gotProj.ID != proj.ID {
		t.Fatalf("GetProjectByID = (%+v,%v)", gotProj, err)
	}

	renamed, err := s.RenameProject(ctx, proj.ID, "Web App")
	if err != nil || renamed.Name != "Web App" {
		t.Fatalf("RenameProject = (%+v,%v)", renamed, err)
	}
	if renamed.Slug != "web" || renamed.PoolID != "pc-acme-web" {
		t.Fatalf("RenameProject changed immutable fields: %+v", renamed)
	}
	if _, err := s.RenameProject(ctx, "00000000-0000-0000-0000-000000000000", "x"); err != ErrNotFound {
		t.Fatalf("RenameProject(missing) = %v, want ErrNotFound", err)
	}

	// Empty project counts zero and deletes.
	if n, err := s.CountActiveOwnershipByProject(ctx, proj.ID); err != nil || n != 0 {
		t.Fatalf("CountActiveOwnershipByProject(empty) = (%d,%v), want 0", n, err)
	}
	if err := s.DeleteProject(ctx, proj.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}
	if _, err := s.GetProjectByID(ctx, proj.ID); err != ErrNotFound {
		t.Fatalf("GetProjectByID after delete = %v, want ErrNotFound", err)
	}
	if err := s.DeleteProject(ctx, proj.ID); err != ErrNotFound {
		t.Fatalf("DeleteProject(missing) = %v, want ErrNotFound", err)
	}
}

func TestOwnershipLifecycle(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetPhase3Tables(t, s)
	t.Cleanup(func() { resetPhase3Tables(t, s) })
	ctx := context.Background()

	ten, err := s.CreateTenant(ctx, CreateTenantParams{Name: "Acme", Slug: "acme"})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	proj, err := s.CreateProject(ctx, CreateProjectParams{TenantID: ten.ID, Name: "Web", Slug: "web", PoolID: "pc-acme-web"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	creator, err := s.CreateUser(ctx, CreateUserParams{Email: "c@b.com", PasswordHash: "h", PasswordAlgo: "argon2id"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// A pending reservation with a real creator.
	pending, err := s.CreateOwnership(ctx, CreateOwnershipParams{
		TenantID: ten.ID, ProjectID: proj.ID, VMID: 101, GuestType: "qemu",
		Node: "pve01", CreatedBy: &creator.ID, Status: "pending",
	})
	if err != nil {
		t.Fatalf("CreateOwnership(pending): %v", err)
	}
	if pending.Status != "pending" || pending.CreatedBy == nil || *pending.CreatedBy != creator.ID {
		t.Fatalf("pending ownership = %+v", pending)
	}

	// An active (backfilled) row with a nil creator.
	active, err := s.CreateOwnership(ctx, CreateOwnershipParams{
		TenantID: ten.ID, ProjectID: proj.ID, VMID: 102, GuestType: "lxc",
		Node: "pve01", CreatedBy: nil, Status: "active",
	})
	if err != nil {
		t.Fatalf("CreateOwnership(active): %v", err)
	}
	if active.CreatedBy != nil {
		t.Fatalf("active ownership CreatedBy = %v, want nil", *active.CreatedBy)
	}

	// Duplicate VMID is rejected by the unique constraint.
	if _, err := s.CreateOwnership(ctx, CreateOwnershipParams{
		TenantID: ten.ID, ProjectID: proj.ID, VMID: 101, GuestType: "qemu", Node: "pve01", Status: "active",
	}); err == nil {
		t.Fatal("duplicate vmid ownership accepted")
	}

	got, err := s.GetOwnershipByVMID(ctx, 101)
	if err != nil || got.ID != pending.ID {
		t.Fatalf("GetOwnershipByVMID(101) = (%+v,%v)", got, err)
	}
	if _, err := s.GetOwnershipByVMID(ctx, 999); err != ErrNotFound {
		t.Fatalf("GetOwnershipByVMID(999) = %v, want ErrNotFound", err)
	}

	// ListActiveVMIDs sees both live rows.
	live, err := s.ListActiveVMIDs(ctx)
	if err != nil || !live[101] || !live[102] || len(live) != 2 {
		t.Fatalf("ListActiveVMIDs = (%v,%v), want {101,102}", live, err)
	}

	// Both live rows count against the project's emptiness gate.
	if n, err := s.CountActiveOwnershipByProject(ctx, proj.ID); err != nil || n != 2 {
		t.Fatalf("CountActiveOwnershipByProject = (%d,%v), want 2", n, err)
	}

	// Finalize the pending reservation.
	if err := s.FinalizeOwnership(ctx, pending.ID, "UPID:pve01:1:2:3:qmcreate:101:root@pam:"); err != nil {
		t.Fatalf("FinalizeOwnership: %v", err)
	}
	got, _ = s.GetOwnershipByVMID(ctx, 101)
	if got.Status != "active" || got.PVEUPID == nil || *got.PVEUPID == "" {
		t.Fatalf("after finalize, ownership = %+v", got)
	}
	// Finalizing again (now active, not pending) is a not-found no-op.
	if err := s.FinalizeOwnership(ctx, pending.ID, "x"); err != ErrNotFound {
		t.Fatalf("FinalizeOwnership(non-pending) = %v, want ErrNotFound", err)
	}

	// Tombstone the active row: it leaves the live set and the emptiness count.
	if err := s.TombstoneOwnership(ctx, active.ID); err != nil {
		t.Fatalf("TombstoneOwnership: %v", err)
	}
	live, _ = s.ListActiveVMIDs(ctx)
	if live[102] {
		t.Fatalf("tombstoned vmid 102 still listed active: %v", live)
	}
	if n, _ := s.CountActiveOwnershipByProject(ctx, proj.ID); n != 1 {
		t.Fatalf("CountActiveOwnershipByProject after tombstone = %d, want 1", n)
	}

	// ListOwnershipByTenant / ByProject return every row (incl. tombstoned).
	byTenant, err := s.ListOwnershipByTenant(ctx, ten.ID)
	if err != nil || len(byTenant) != 2 {
		t.Fatalf("ListOwnershipByTenant = (%d rows,%v), want 2", len(byTenant), err)
	}
	byProject, err := s.ListOwnershipByProject(ctx, proj.ID)
	if err != nil || len(byProject) != 2 {
		t.Fatalf("ListOwnershipByProject = (%d rows,%v), want 2", len(byProject), err)
	}

	// A non-empty project cannot be deleted-then-orphaned by our helper: the
	// caller checks CountActiveOwnershipByProject first (still 1 here).
	if n, _ := s.CountActiveOwnershipByProject(ctx, proj.ID); n == 0 {
		t.Fatal("project should still count one live ownership row")
	}

	// Release a fresh pending reservation frees its VMID for reuse.
	res, err := s.CreateOwnership(ctx, CreateOwnershipParams{
		TenantID: ten.ID, ProjectID: proj.ID, VMID: 103, GuestType: "qemu", Node: "pve01", Status: "pending",
	})
	if err != nil {
		t.Fatalf("CreateOwnership(103): %v", err)
	}
	if err := s.ReleaseOwnership(ctx, res.ID); err != nil {
		t.Fatalf("ReleaseOwnership: %v", err)
	}
	if _, err := s.GetOwnershipByVMID(ctx, 103); err != ErrNotFound {
		t.Fatalf("released vmid 103 still present: %v", err)
	}
	// The VMID is genuinely free — a new insert with the same VMID succeeds.
	if _, err := s.CreateOwnership(ctx, CreateOwnershipParams{
		TenantID: ten.ID, ProjectID: proj.ID, VMID: 103, GuestType: "qemu", Node: "pve01", Status: "active",
	}); err != nil {
		t.Fatalf("reuse released vmid 103: %v", err)
	}

	// A TOMBSTONED VMID is free too: CreateOwnership revives the row in place
	// (same id, fresh status) instead of colliding on the unique constraint —
	// this is what lets a deleted guest's VMID be reused (create→delete→create).
	// vmid 102 was tombstoned above.
	revived, err := s.CreateOwnership(ctx, CreateOwnershipParams{
		TenantID: ten.ID, ProjectID: proj.ID, VMID: 102, GuestType: "lxc",
		Node: "pve01", CreatedBy: &creator.ID, Status: "pending",
	})
	if err != nil {
		t.Fatalf("revive tombstoned vmid 102: %v", err)
	}
	if revived.ID != active.ID {
		t.Fatalf("revive should reuse the tombstoned row id: got %s, want %s", revived.ID, active.ID)
	}
	if revived.Status != "pending" || revived.CreatedBy == nil || *revived.CreatedBy != creator.ID {
		t.Fatalf("revived ownership = %+v, want pending with the new creator", revived)
	}
	// The revived row is live again, so a further create over it is a real conflict.
	if _, err := s.CreateOwnership(ctx, CreateOwnershipParams{
		TenantID: ten.ID, ProjectID: proj.ID, VMID: 102, GuestType: "lxc", Node: "pve01", Status: "active",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("create over a revived live row = %v, want ErrConflict", err)
	}
}

// A duplicate slug (tenant or project) and a duplicate pool_id (globally, per
// migration 000002) and a duplicate VMID all surface as the ErrConflict sentinel
// — the 409 backbone for the create paths, not a raw pg error.
func TestCreateUniqueViolationsMapToErrConflict(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetPhase3Tables(t, s)
	t.Cleanup(func() { resetPhase3Tables(t, s) })
	ctx := context.Background()

	tA, err := s.CreateTenant(ctx, CreateTenantParams{Name: "Acme", Slug: "acme"})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	// Duplicate tenant slug → ErrConflict.
	if _, err := s.CreateTenant(ctx, CreateTenantParams{Name: "Acme Two", Slug: "acme"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate tenant slug = %v, want ErrConflict", err)
	}

	tB, err := s.CreateTenant(ctx, CreateTenantParams{Name: "Beta", Slug: "beta"})
	if err != nil {
		t.Fatalf("CreateTenant(beta): %v", err)
	}
	pA, err := s.CreateProject(ctx, CreateProjectParams{TenantID: tA.ID, Name: "Web", Slug: "web", PoolID: "pc-acme-web"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	// Duplicate (tenant_id, slug) within the same tenant → ErrConflict.
	if _, err := s.CreateProject(ctx, CreateProjectParams{TenantID: tA.ID, Name: "Web Dup", Slug: "web", PoolID: "pc-acme-web-2"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate (tenant,slug) project = %v, want ErrConflict", err)
	}
	// Duplicate pool_id from a DIFFERENT tenant → ErrConflict (UNIQUE(pool_id)):
	// the cross-tenant pool-collision guard that migration 000002 adds.
	if _, err := s.CreateProject(ctx, CreateProjectParams{TenantID: tB.ID, Name: "Steal", Slug: "steal", PoolID: "pc-acme-web"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-tenant duplicate pool_id = %v, want ErrConflict (migration 000002 UNIQUE(pool_id))", err)
	}

	// Duplicate VMID reservation → ErrConflict.
	if _, err := s.CreateOwnership(ctx, CreateOwnershipParams{TenantID: tA.ID, ProjectID: pA.ID, VMID: 900, GuestType: "qemu", Node: "pve01", Status: "active"}); err != nil {
		t.Fatalf("CreateOwnership: %v", err)
	}
	if _, err := s.CreateOwnership(ctx, CreateOwnershipParams{TenantID: tA.ID, ProjectID: pA.ID, VMID: 900, GuestType: "qemu", Node: "pve01", Status: "active"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate vmid ownership = %v, want ErrConflict", err)
	}
}

// ListMembershipsByScopes returns every grant across a batch of scope ids in one
// query (the members-list N+1 fix), and treats an empty batch as an empty result.
func TestListMembershipsByScopes(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetPhase3Tables(t, s)
	t.Cleanup(func() { resetPhase3Tables(t, s) })
	ctx := context.Background()

	ten, _ := s.CreateTenant(ctx, CreateTenantParams{Name: "Acme", Slug: "acme"})
	p1, _ := s.CreateProject(ctx, CreateProjectParams{TenantID: ten.ID, Name: "P1", Slug: "p1", PoolID: "pc-acme-p1"})
	p2, _ := s.CreateProject(ctx, CreateProjectParams{TenantID: ten.ID, Name: "P2", Slug: "p2", PoolID: "pc-acme-p2"})
	p3, _ := s.CreateProject(ctx, CreateProjectParams{TenantID: ten.ID, Name: "P3", Slug: "p3", PoolID: "pc-acme-p3"})
	u1, _ := s.CreateUser(ctx, CreateUserParams{Email: "u1@b.com", DisplayName: "One", PasswordHash: "h", PasswordAlgo: "argon2id"})
	u2, _ := s.CreateUser(ctx, CreateUserParams{Email: "u2@b.com", DisplayName: "Two", PasswordHash: "h", PasswordAlgo: "argon2id"})
	mustMembership(t, s, u1.ID, "project", p1.ID, "contributor")
	mustMembership(t, s, u2.ID, "project", p2.ID, "reader")
	// p3 has no members; a tenant-scope grant must NOT leak into a project batch.
	mustMembership(t, s, u1.ID, "tenant", ten.ID, "owner")

	got, err := s.ListMembershipsByScopes(ctx, "project", []string{p1.ID, p2.ID, p3.ID})
	if err != nil {
		t.Fatalf("ListMembershipsByScopes: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("batched project memberships = %d, want 2 (p1+p2; p3 empty, tenant grant excluded) — %+v", len(got), got)
	}
	byScope := map[string]Membership{}
	for _, m := range got {
		byScope[m.ScopeID] = m
	}
	if byScope[p1.ID].Role != "contributor" || byScope[p2.ID].Role != "reader" {
		t.Fatalf("batched roles = %+v, want p1=contributor p2=reader", byScope)
	}

	// Empty batch is a cheap empty result (no query, no error).
	if out, err := s.ListMembershipsByScopes(ctx, "project", nil); err != nil || len(out) != 0 {
		t.Fatalf("ListMembershipsByScopes(nil) = (%v,%v), want empty", out, err)
	}
}

func TestEffectiveRolesAndTenantsForUser(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetPhase3Tables(t, s)
	t.Cleanup(func() { resetPhase3Tables(t, s) })
	ctx := context.Background()

	tA, _ := s.CreateTenant(ctx, CreateTenantParams{Name: "Acme", Slug: "acme"})
	tB, _ := s.CreateTenant(ctx, CreateTenantParams{Name: "Beta", Slug: "beta"})
	tC, _ := s.CreateTenant(ctx, CreateTenantParams{Name: "Gamma", Slug: "gamma"})
	pA, _ := s.CreateProject(ctx, CreateProjectParams{TenantID: tA.ID, Name: "Web", Slug: "web", PoolID: "pc-acme-web"})
	pB, _ := s.CreateProject(ctx, CreateProjectParams{TenantID: tB.ID, Name: "Api", Slug: "api", PoolID: "pc-beta-api"})

	u, err := s.CreateUser(ctx, CreateUserParams{Email: "u@b.com", DisplayName: "Ada", PasswordHash: "h", PasswordAlgo: "argon2id"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}

	// Tenant A: reader at tenant scope + contributor at project scope.
	mustMembership(t, s, u.ID, "tenant", tA.ID, "reader")
	mustMembership(t, s, u.ID, "project", pA.ID, "contributor")
	// Tenant B: project-only membership (reader), no tenant-scope role.
	mustMembership(t, s, u.ID, "project", pB.ID, "reader")
	// Tenant C: no membership at all.

	// Effective roles in A: tenant=reader, project pA=contributor.
	tenantRole, projRoles, err := s.GetEffectiveRoles(ctx, u.ID, tA.ID)
	if err != nil {
		t.Fatalf("GetEffectiveRoles(A): %v", err)
	}
	if tenantRole != "reader" {
		t.Fatalf("tenant role in A = %q, want reader", tenantRole)
	}
	if projRoles[pA.ID] != "contributor" {
		t.Fatalf("project role for pA = %q, want contributor", projRoles[pA.ID])
	}

	// Effective roles in B: no tenant role, project pB=reader.
	tenantRole, projRoles, err = s.GetEffectiveRoles(ctx, u.ID, tB.ID)
	if err != nil {
		t.Fatalf("GetEffectiveRoles(B): %v", err)
	}
	if tenantRole != "" {
		t.Fatalf("tenant role in B = %q, want empty (project-only member)", tenantRole)
	}
	if projRoles[pB.ID] != "reader" {
		t.Fatalf("project role for pB = %q, want reader", projRoles[pB.ID])
	}

	// Effective roles in C: nothing.
	tenantRole, projRoles, err = s.GetEffectiveRoles(ctx, u.ID, tC.ID)
	if err != nil {
		t.Fatalf("GetEffectiveRoles(C): %v", err)
	}
	if tenantRole != "" || len(projRoles) != 0 {
		t.Fatalf("effective roles in C = (%q,%v), want empty", tenantRole, projRoles)
	}

	// ListTenantsForUser: A (max(reader,contributor)=contributor) + B (reader),
	// never C. Ordered by slug: acme, beta.
	tenants, err := s.ListTenantsForUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListTenantsForUser: %v", err)
	}
	if len(tenants) != 2 {
		t.Fatalf("ListTenantsForUser returned %d tenants, want 2 (%+v)", len(tenants), tenants)
	}
	if tenants[0].Slug != "acme" || tenants[0].Role != "contributor" {
		t.Fatalf("tenant[0] = %+v, want acme/contributor (project role adds)", tenants[0])
	}
	if tenants[1].Slug != "beta" || tenants[1].Role != "reader" {
		t.Fatalf("tenant[1] = %+v, want beta/reader", tenants[1])
	}

	// ListMembershipsByScope on tenant A returns the tenant-scope reader grant.
	mem, err := s.ListMembershipsByScope(ctx, "tenant", tA.ID)
	if err != nil || len(mem) != 1 || mem[0].Role != "reader" {
		t.Fatalf("ListMembershipsByScope(tenant,A) = (%+v,%v)", mem, err)
	}
}

func TestListUsersByIDs(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetPhase3Tables(t, s)
	t.Cleanup(func() { resetPhase3Tables(t, s) })
	ctx := context.Background()

	u1, _ := s.CreateUser(ctx, CreateUserParams{Email: "a@b.com", DisplayName: "Ada", PasswordHash: "h", PasswordAlgo: "argon2id"})
	u2, _ := s.CreateUser(ctx, CreateUserParams{Email: "b@b.com", DisplayName: "Bee", PasswordHash: "h", PasswordAlgo: "argon2id"})

	// Empty input is a cheap empty map, not a query.
	if m, err := s.ListUsersByIDs(ctx, nil); err != nil || len(m) != 0 {
		t.Fatalf("ListUsersByIDs(nil) = (%v,%v), want empty", m, err)
	}

	got, err := s.ListUsersByIDs(ctx, []string{u1.ID, u2.ID, "00000000-0000-0000-0000-000000000000"})
	if err != nil {
		t.Fatalf("ListUsersByIDs: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("ListUsersByIDs returned %d users, want 2", len(got))
	}
	if got[u1.ID].DisplayName != "Ada" || got[u2.ID].DisplayName != "Bee" {
		t.Fatalf("ListUsersByIDs map = %+v", got)
	}
}

func TestSetSessionActiveTenant(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetPhase3Tables(t, s)
	t.Cleanup(func() { resetPhase3Tables(t, s) })
	ctx := context.Background()

	u, err := s.CreateUser(ctx, CreateUserParams{Email: "s@b.com", PasswordHash: "h", PasswordAlgo: "argon2id"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	ten, err := s.CreateTenant(ctx, CreateTenantParams{Name: "Acme", Slug: "acme"})
	if err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	sess, err := s.CreateSession(ctx, CreateSessionParams{
		UserID: u.ID, TokenHash: "hash-active-tenant", AbsoluteExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ActiveTenantID != nil {
		t.Fatalf("new session ActiveTenantID = %v, want nil", *sess.ActiveTenantID)
	}

	// Set the active tenant, then read it back.
	if err := s.SetSessionActiveTenant(ctx, sess.ID, &ten.ID); err != nil {
		t.Fatalf("SetSessionActiveTenant: %v", err)
	}
	got, err := s.GetSessionByTokenHash(ctx, "hash-active-tenant")
	if err != nil || got.ActiveTenantID == nil || *got.ActiveTenantID != ten.ID {
		t.Fatalf("after set, ActiveTenantID = %v (err %v), want %s", got.ActiveTenantID, err, ten.ID)
	}

	// Clear it (nil) — the tenant switch back to "no active tenant".
	if err := s.SetSessionActiveTenant(ctx, sess.ID, nil); err != nil {
		t.Fatalf("SetSessionActiveTenant(nil): %v", err)
	}
	got, _ = s.GetSessionByTokenHash(ctx, "hash-active-tenant")
	if got.ActiveTenantID != nil {
		t.Fatalf("after clear, ActiveTenantID = %v, want nil", *got.ActiveTenantID)
	}

	// A missing session id is ErrNotFound.
	if err := s.SetSessionActiveTenant(ctx, "00000000-0000-0000-0000-000000000000", &ten.ID); err != ErrNotFound {
		t.Fatalf("SetSessionActiveTenant(missing) = %v, want ErrNotFound", err)
	}
}

func mustMembership(t *testing.T, s *PgStore, userID, scopeType, scopeID, role string) {
	t.Helper()
	if _, err := s.CreateMembership(context.Background(), CreateMembershipParams{
		UserID: userID, ScopeType: scopeType, ScopeID: scopeID, Role: role,
	}); err != nil {
		t.Fatalf("CreateMembership(%s %s %s): %v", scopeType, role, scopeID, err)
	}
}
