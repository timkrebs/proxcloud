package auth

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// captureLogger returns a slog.Logger writing to buf so tests can assert on the
// (loud) env-admin seed log lines and prove no secret material is emitted.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// TestSeedEnvAdminSeedsOnEmptyDB is the env-admin cutover happy path: a fresh
// users table + ADMIN_USER/ADMIN_PASSWORD seeds exactly one platform-admin,
// deriving the email as <user>@proxcloud.local, storing a bcrypt hash, logging
// loudly at WARN, and never printing the password.
func TestSeedEnvAdminSeedsOnEmptyDB(t *testing.T) {
	fs := newFakeStore()
	var buf bytes.Buffer
	ctx := context.Background()

	if err := SeedEnvAdmin(ctx, fs, "admin", "", "s3cr3t-plaintext", captureLogger(&buf)); err != nil {
		t.Fatalf("SeedEnvAdmin: %v", err)
	}

	if n, _ := fs.CountUsers(ctx); n != 1 {
		t.Fatalf("user count = %d, want 1", n)
	}

	// Email derived as <user>@proxcloud.local; literal "admin" is NOT an email.
	if _, err := fs.GetUserByEmail(ctx, "admin"); err != store.ErrNotFound {
		t.Fatalf("literal 'admin' lookup = %v, want ErrNotFound", err)
	}
	u, err := fs.GetUserByEmail(ctx, "admin@proxcloud.local")
	if err != nil {
		t.Fatalf("GetUserByEmail(admin@proxcloud.local): %v", err)
	}
	if !u.IsPlatformAdmin {
		t.Fatal("seeded user is not a platform admin")
	}
	if u.PasswordAlgo == nil || *u.PasswordAlgo != AlgoBcrypt {
		t.Fatalf("seeded algo = %v, want bcrypt", u.PasswordAlgo)
	}
	if u.PasswordHash == nil || !CheckPassword(*u.PasswordHash, "s3cr3t-plaintext") {
		t.Fatal("seeded bcrypt hash does not verify the plaintext admin password")
	}

	// It got an Owner membership on the default tenant.
	mems, _ := fs.ListMembershipsByUser(ctx, u.ID)
	if len(mems) != 1 || mems[0].Role != "owner" || mems[0].ScopeType != "tenant" {
		t.Fatalf("membership = %+v, want single tenant owner", mems)
	}

	logs := buf.String()
	if !strings.Contains(logs, "SEEDED PLATFORM ADMIN FROM ADMIN_*") {
		t.Fatalf("missing loud seed WARN; log was:\n%s", logs)
	}
	if strings.Contains(logs, "s3cr3t-plaintext") {
		t.Fatalf("SECRET LEAK: plaintext password found in logs:\n%s", logs)
	}
}

// TestSeedEnvAdminEmailWithAt uses ADMIN_USER verbatim when it already contains
// an '@' (no @proxcloud.local suffix appended).
func TestSeedEnvAdminEmailWithAt(t *testing.T) {
	fs := newFakeStore()
	var buf bytes.Buffer
	ctx := context.Background()

	if err := SeedEnvAdmin(ctx, fs, "ops@example.com", "", "another-strong-pass", captureLogger(&buf)); err != nil {
		t.Fatalf("SeedEnvAdmin: %v", err)
	}
	if _, err := fs.GetUserByEmail(ctx, "ops@example.com"); err != nil {
		t.Fatalf("GetUserByEmail(ops@example.com): %v", err)
	}
}

// TestSeedEnvAdminIdempotent proves the seed acts once: a second boot with
// ADMIN_* still set does NOT reseed, keeps the count at 1, and logs the loud
// "ignored" WARN instead.
func TestSeedEnvAdminIdempotent(t *testing.T) {
	fs := newFakeStore()
	ctx := context.Background()

	if err := SeedEnvAdmin(ctx, fs, "admin", "", "s3cr3t-plaintext", captureLogger(&bytes.Buffer{})); err != nil {
		t.Fatalf("SeedEnvAdmin (first): %v", err)
	}

	var buf2 bytes.Buffer
	if err := SeedEnvAdmin(ctx, fs, "admin", "", "s3cr3t-plaintext", captureLogger(&buf2)); err != nil {
		t.Fatalf("SeedEnvAdmin (second): %v", err)
	}
	if n, _ := fs.CountUsers(ctx); n != 1 {
		t.Fatalf("user count after second seed = %d, want 1 (no reseed)", n)
	}
	if !strings.Contains(buf2.String(), "ADMIN_* is set but ignored") {
		t.Fatalf("missing 'ignored' WARN on second boot; log was:\n%s", buf2.String())
	}
}

// TestSeedEnvAdminNoEnvNoUser: a fresh DB with no ADMIN_* seeds nothing — the
// first-run bootstrap endpoint is the path instead.
func TestSeedEnvAdminNoEnvNoUser(t *testing.T) {
	fs := newFakeStore()
	ctx := context.Background()
	if err := SeedEnvAdmin(ctx, fs, "", "", "", captureLogger(&bytes.Buffer{})); err != nil {
		t.Fatalf("SeedEnvAdmin: %v", err)
	}
	if n, _ := fs.CountUsers(ctx); n != 0 {
		t.Fatalf("user count = %d, want 0 (nothing seeded)", n)
	}
}

// TestSeedEnvAdminIgnoredWhenUsersExist: ADMIN_* set but a user already exists
// (e.g. created via bootstrap) — no reseed, and the loud "ignored" WARN fires.
func TestSeedEnvAdminIgnoredWhenUsersExist(t *testing.T) {
	fs := newFakeStore()
	ctx := context.Background()
	if _, err := fs.CreateUser(ctx, store.CreateUserParams{
		Email: "founder@example.com", PasswordHash: "x", PasswordAlgo: AlgoArgon2id, IsPlatformAdmin: true,
	}); err != nil {
		t.Fatalf("seed existing user: %v", err)
	}

	var buf bytes.Buffer
	if err := SeedEnvAdmin(ctx, fs, "admin", "", "s3cr3t-plaintext", captureLogger(&buf)); err != nil {
		t.Fatalf("SeedEnvAdmin: %v", err)
	}
	if n, _ := fs.CountUsers(ctx); n != 1 {
		t.Fatalf("user count = %d, want 1 (existing user untouched, no seed)", n)
	}
	if _, err := fs.GetUserByEmail(ctx, "admin@proxcloud.local"); err != store.ErrNotFound {
		t.Fatal("env admin was seeded even though a user already existed")
	}
	if !strings.Contains(buf.String(), "ADMIN_* is set but ignored") {
		t.Fatalf("missing 'ignored' WARN; log was:\n%s", buf.String())
	}
}

// TestSeedEnvAdminRejectsBadHash: a malformed ADMIN_PASSWORD_HASH fails fast so
// a broken credential never silently produces an unusable admin.
func TestSeedEnvAdminRejectsBadHash(t *testing.T) {
	fs := newFakeStore()
	ctx := context.Background()
	err := SeedEnvAdmin(ctx, fs, "admin", "not-a-bcrypt-hash", "", captureLogger(&bytes.Buffer{}))
	if err == nil {
		t.Fatal("SeedEnvAdmin accepted an invalid bcrypt ADMIN_PASSWORD_HASH")
	}
	if n, _ := fs.CountUsers(ctx); n != 0 {
		t.Fatalf("user count = %d, want 0 after a failed seed", n)
	}
}
