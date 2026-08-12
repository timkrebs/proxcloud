package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"testing"

	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
	"github.com/timkrebs9/proxcloud/backend/internal/store/storetest"
)

func osArgs() []string        { return os.Args }
func setOSArgs(args []string) { os.Args = args }

// discardLog is a logger that drops everything — the command helpers log, but
// the tests assert on return values/store state, not log output.
func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeHasher is a deterministic stand-in for *auth.PasswordHasher: it records
// that Hash was called and returns a fixed non-empty string. seedSmoke only
// needs the hash to be stored, not verified.
type fakeHasher struct{ calls int }

func (h *fakeHasher) Hash(pw string) (string, error) {
	h.calls++
	return "argon2id$fake$" + pw, nil
}

// errHasher always fails, to exercise the hash-error path.
type errHasher struct{}

func (errHasher) Hash(string) (string, error) { return "", errors.New("boom") }

// --- migrate ------------------------------------------------------------------

// stubMigrator lets applyMigrations be tested without a database.
type stubMigrator struct {
	version uint
	err     error
	calls   int
}

func (m *stubMigrator) RunMigrations() (uint, error) {
	m.calls++
	return m.version, m.err
}

func TestApplyMigrations(t *testing.T) {
	tests := []struct {
		name        string
		version     uint
		err         error
		wantVersion uint
		wantErr     bool
	}{
		{name: "reports version on success", version: 4, wantVersion: 4},
		{name: "propagates failure", version: 0, err: errors.New("dirty"), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := &stubMigrator{version: tc.version, err: tc.err}
			got, err := applyMigrations(m, discardLog())
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantVersion {
				t.Fatalf("version = %d, want %d", got, tc.wantVersion)
			}
			if m.calls != 1 {
				t.Fatalf("RunMigrations called %d times, want 1", m.calls)
			}
		})
	}
}

// TestApplyMigrationsAgainstFakeStore confirms the migrate command reports the
// version the store returns (the fake reports 3).
func TestApplyMigrationsAgainstFakeStore(t *testing.T) {
	got, err := applyMigrations(storetest.New(), discardLog())
	if err != nil {
		t.Fatalf("applyMigrations: %v", err)
	}
	if got != 3 {
		t.Fatalf("version = %d, want 3 (fake store)", got)
	}
}

// --- seed-smoke ---------------------------------------------------------------

const (
	testSmokeEmail = "smoke@proxcloud.local"
	testSmokePass  = "hunter2hunter2"
)

func TestSeedSmoke_CreatesThenIdempotent(t *testing.T) {
	st := storetest.New()
	hasher := &fakeHasher{}
	ctx := context.Background()

	// First run: everything is created.
	first, err := seedSmoke(ctx, st, hasher, testSmokeEmail, testSmokePass, discardLog())
	if err != nil {
		t.Fatalf("first seedSmoke: %v", err)
	}
	for label, created := range map[string]bool{
		"tenant":     first.CreatedTenant,
		"project":    first.CreatedProject,
		"user":       first.CreatedUser,
		"membership": first.CreatedMembership,
		"quota":      first.CreatedQuota,
	} {
		if !created {
			t.Errorf("first run: expected %s to be created", label)
		}
	}
	if hasher.calls != 1 {
		t.Fatalf("hasher called %d times on first run, want 1", hasher.calls)
	}

	// Second run: nothing new is created, ids are stable.
	second, err := seedSmoke(ctx, st, hasher, testSmokeEmail, testSmokePass, discardLog())
	if err != nil {
		t.Fatalf("second seedSmoke: %v", err)
	}
	for label, created := range map[string]bool{
		"tenant":     second.CreatedTenant,
		"project":    second.CreatedProject,
		"user":       second.CreatedUser,
		"membership": second.CreatedMembership,
		"quota":      second.CreatedQuota,
	} {
		if created {
			t.Errorf("second run: expected %s to be skipped (idempotent)", label)
		}
	}
	if hasher.calls != 1 {
		t.Errorf("hasher called %d times total, want 1 (no re-hash on idempotent run)", hasher.calls)
	}
	if second.TenantID != first.TenantID || second.ProjectID != first.ProjectID || second.UserID != first.UserID {
		t.Errorf("ids changed across runs: %+v vs %+v", first, second)
	}

	// Exactly one tenant/project/user exists.
	tenants, _ := st.ListTenants(ctx)
	if len(tenants) != 1 {
		t.Errorf("tenant count = %d, want 1", len(tenants))
	}
	projects, _ := st.ListProjectsByTenant(ctx, first.TenantID)
	if len(projects) != 1 {
		t.Errorf("project count = %d, want 1", len(projects))
	}
	if got := projects[0].PoolID; got != smokePoolID {
		t.Errorf("project pool id = %q, want %q", got, smokePoolID)
	}
	if _, err := st.GetUserByEmail(ctx, testSmokeEmail); err != nil {
		t.Errorf("GetUserByEmail: %v", err)
	}
}

// TestSeedSmoke_MembershipRoleAndScope pins the least-privilege guarantee: the
// smoke user holds exactly one membership, Contributor at the tenant scope —
// never Owner, and the user is not a platform admin.
func TestSeedSmoke_MembershipRoleAndScope(t *testing.T) {
	st := storetest.New()
	ctx := context.Background()

	res, err := seedSmoke(ctx, st, &fakeHasher{}, testSmokeEmail, testSmokePass, discardLog())
	if err != nil {
		t.Fatalf("seedSmoke: %v", err)
	}

	memberships, err := st.ListMembershipsByUser(ctx, res.UserID)
	if err != nil {
		t.Fatalf("ListMembershipsByUser: %v", err)
	}
	if len(memberships) != 1 {
		t.Fatalf("membership count = %d, want 1", len(memberships))
	}
	m := memberships[0]
	if m.Role != authz.RoleContributorStr {
		t.Errorf("role = %q, want %q", m.Role, authz.RoleContributorStr)
	}
	if m.Role == authz.RoleOwnerStr {
		t.Errorf("smoke user must not be Owner")
	}
	if m.ScopeType != "tenant" || m.ScopeID != res.TenantID {
		t.Errorf("scope = %s/%s, want tenant/%s", m.ScopeType, m.ScopeID, res.TenantID)
	}

	user, err := st.GetUserByID(ctx, res.UserID)
	if err != nil {
		t.Fatalf("GetUserByID: %v", err)
	}
	if user.IsPlatformAdmin {
		t.Errorf("smoke user must not be a platform admin")
	}
	if user.TOTPEnabled {
		t.Errorf("smoke user must have TOTP disabled")
	}
}

// TestSeedSmoke_SetsTinyQuota confirms the smoke project gets the small caps.
func TestSeedSmoke_SetsTinyQuota(t *testing.T) {
	st := storetest.New()
	ctx := context.Background()

	res, err := seedSmoke(ctx, st, &fakeHasher{}, testSmokeEmail, testSmokePass, discardLog())
	if err != nil {
		t.Fatalf("seedSmoke: %v", err)
	}
	q, err := st.GetQuota(ctx, "project", res.ProjectID)
	if err != nil {
		t.Fatalf("GetQuota: %v", err)
	}
	if q.MaxVCPU == nil || *q.MaxVCPU != smokeQuotaVCPU {
		t.Errorf("MaxVCPU = %v, want %d", q.MaxVCPU, smokeQuotaVCPU)
	}
	if q.MaxCount == nil || *q.MaxCount != smokeQuotaCount {
		t.Errorf("MaxCount = %v, want %d", q.MaxCount, smokeQuotaCount)
	}
}

// TestSeedSmoke_HashError surfaces a hashing failure instead of creating a user
// with an empty password hash.
func TestSeedSmoke_HashError(t *testing.T) {
	st := storetest.New()
	ctx := context.Background()

	if _, err := seedSmoke(ctx, st, errHasher{}, testSmokeEmail, testSmokePass, discardLog()); err == nil {
		t.Fatal("expected error from failing hasher, got nil")
	}
	// The user must not have been created.
	if _, err := st.GetUserByEmail(ctx, testSmokeEmail); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("expected no smoke user after hash failure, got err=%v", err)
	}
}

// TestSubcommand covers the dispatch selector without spawning the process.
func TestSubcommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "no args -> serve", args: []string{"proxcloud"}, want: ""},
		{name: "migrate", args: []string{"proxcloud", "migrate"}, want: "migrate"},
		{name: "seed-smoke", args: []string{"proxcloud", "seed-smoke"}, want: "seed-smoke"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			orig := osArgs()
			setOSArgs(tc.args)
			defer setOSArgs(orig)
			if got := subcommand(); got != tc.want {
				t.Fatalf("subcommand() = %q, want %q", got, tc.want)
			}
		})
	}
}
