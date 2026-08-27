//go:build integration

package store

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// guardDestructive refuses to run a users-wiping test against a database whose
// name does not look ephemeral, unless PROXCLOUD_ALLOW_DESTRUCTIVE_TESTS=1.
// resetIdentityTables issues DELETE FROM users (cascading to sessions and
// memberships), so pointing DATABASE_URL at a real database would silently
// empty it. The migrations/seed integration tests do not call this, so they
// still run read-only against a shared dev DB.
func guardDestructive(t *testing.T) {
	t.Helper()
	if os.Getenv("PROXCLOUD_ALLOW_DESTRUCTIVE_TESTS") == "1" {
		return
	}
	u, err := url.Parse(os.Getenv("DATABASE_URL"))
	if err != nil {
		return
	}
	name := strings.Trim(u.Path, "/")
	for _, hint := range []string{"test", "scratch", "qa", "ci", "tmp"} {
		if strings.Contains(name, hint) {
			return
		}
	}
	t.Skipf("refusing to wipe users in non-ephemeral database %q; point DATABASE_URL at a scratch DB or set PROXCLOUD_ALLOW_DESTRUCTIVE_TESTS=1", name)
}

// resetIdentityTables clears users (cascading to sessions + memberships) so
// each identity integration test starts clean. Guarded against non-ephemeral
// databases (see guardDestructive) and gated by the DATABASE_URL skip in
// requireStore.
func resetIdentityTables(t *testing.T, s *PgStore) {
	t.Helper()
	guardDestructive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := s.pool.Exec(ctx, `DELETE FROM users`); err != nil {
		t.Fatalf("reset users: %v", err)
	}
}

func TestUserLifecycle(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetIdentityTables(t, s)
	t.Cleanup(func() { resetIdentityTables(t, s) })

	ctx := context.Background()

	if n, err := s.CountUsers(ctx); err != nil || n != 0 {
		t.Fatalf("CountUsers on empty = (%d,%v), want (0,nil)", n, err)
	}

	u, err := s.CreateUser(ctx, CreateUserParams{
		Email: "Admin@Example.com", DisplayName: "Admin", PasswordHash: "hash1", PasswordAlgo: "argon2id", IsPlatformAdmin: true,
	})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	if u.ID == "" || !u.IsPlatformAdmin || u.PasswordAlgo == nil || *u.PasswordAlgo != "argon2id" {
		t.Fatalf("CreateUser returned %+v", u)
	}

	// Case-insensitive email lookup.
	got, err := s.GetUserByEmail(ctx, "admin@EXAMPLE.com")
	if err != nil || got.ID != u.ID {
		t.Fatalf("GetUserByEmail (ci) = (%+v,%v)", got, err)
	}

	byID, err := s.GetUserByID(ctx, u.ID)
	if err != nil || byID.Email != "Admin@Example.com" {
		t.Fatalf("GetUserByID = (%+v,%v)", byID, err)
	}

	if err := s.UpdatePasswordHash(ctx, u.ID, "hash2", "argon2id"); err != nil {
		t.Fatalf("UpdatePasswordHash: %v", err)
	}
	byID, _ = s.GetUserByID(ctx, u.ID)
	if byID.PasswordHash == nil || *byID.PasswordHash != "hash2" {
		t.Fatalf("password hash not updated: %v", byID.PasswordHash)
	}

	if n, err := s.CountUsers(ctx); err != nil || n != 1 {
		t.Fatalf("CountUsers = (%d,%v), want 1", n, err)
	}

	// Duplicate email (case-insensitive) is rejected by the unique index.
	if _, err := s.CreateUser(ctx, CreateUserParams{Email: "ADMIN@example.com", PasswordHash: "x", PasswordAlgo: "argon2id"}); err == nil {
		t.Fatal("duplicate email accepted")
	}

	if _, err := s.GetUserByEmail(ctx, "nobody@example.com"); err != ErrNotFound {
		t.Fatalf("GetUserByEmail(missing) = %v, want ErrNotFound", err)
	}
}

func TestMembershipLifecycle(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetIdentityTables(t, s)
	t.Cleanup(func() { resetIdentityTables(t, s) })
	ctx := context.Background()

	u, err := s.CreateUser(ctx, CreateUserParams{Email: "owner@b.com", PasswordHash: "h", PasswordAlgo: "argon2id"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tenant, err := s.GetTenantBySlug(ctx, "default")
	if err != nil {
		t.Fatalf("GetTenantBySlug: %v", err)
	}
	m, err := s.CreateMembership(ctx, CreateMembershipParams{UserID: u.ID, ScopeType: "tenant", ScopeID: tenant.ID, Role: "owner"})
	if err != nil {
		t.Fatalf("CreateMembership: %v", err)
	}
	if m.Role != "owner" || m.ScopeID != tenant.ID {
		t.Fatalf("membership = %+v", m)
	}
	mems, err := s.ListMembershipsByUser(ctx, u.ID)
	if err != nil || len(mems) != 1 || mems[0].ID != m.ID {
		t.Fatalf("ListMembershipsByUser = (%+v,%v)", mems, err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetIdentityTables(t, s)
	t.Cleanup(func() { resetIdentityTables(t, s) })
	ctx := context.Background()

	u, err := s.CreateUser(ctx, CreateUserParams{Email: "sess@b.com", PasswordHash: "h", PasswordAlgo: "argon2id"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	ip, ua := "203.0.113.7", "curl/8"

	sess, err := s.CreateSession(ctx, CreateSessionParams{
		UserID: u.ID, TokenHash: "hash-A", AbsoluteExpiresAt: time.Now().Add(time.Hour), IP: &ip, UserAgent: &ua,
	})
	if err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	if sess.ID == "" || sess.IP == nil || *sess.IP != ip {
		t.Fatalf("CreateSession returned %+v", sess)
	}

	got, err := s.GetSessionByTokenHash(ctx, "hash-A")
	if err != nil || got.ID != sess.ID {
		t.Fatalf("GetSessionByTokenHash = (%+v,%v)", got, err)
	}

	// TouchSession bumps last_seen_at.
	newSeen := time.Now().Add(30 * time.Second).Truncate(time.Millisecond)
	if err := s.TouchSession(ctx, sess.ID, newSeen); err != nil {
		t.Fatalf("TouchSession: %v", err)
	}
	got, _ = s.GetSessionByTokenHash(ctx, "hash-A")
	if got.LastSeenAt.Before(sess.LastSeenAt) {
		t.Fatalf("last_seen_at not advanced: %v -> %v", sess.LastSeenAt, got.LastSeenAt)
	}

	// A second live session + one absolute-expired session for the same user.
	if _, err := s.CreateSession(ctx, CreateSessionParams{UserID: u.ID, TokenHash: "hash-B", AbsoluteExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("CreateSession B: %v", err)
	}
	if _, err := s.CreateSession(ctx, CreateSessionParams{UserID: u.ID, TokenHash: "hash-expired", AbsoluteExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatalf("CreateSession expired: %v", err)
	}

	// ListSessionsByUser excludes the absolute-expired one.
	live, err := s.ListSessionsByUser(ctx, u.ID)
	if err != nil {
		t.Fatalf("ListSessionsByUser: %v", err)
	}
	if len(live) != 2 {
		t.Fatalf("live sessions = %d, want 2 (expired excluded)", len(live))
	}

	// RevokeOtherUserSessions keeps only hash-A's session.
	if err := s.RevokeOtherUserSessions(ctx, u.ID, sess.ID); err != nil {
		t.Fatalf("RevokeOtherUserSessions: %v", err)
	}
	live, _ = s.ListSessionsByUser(ctx, u.ID)
	if len(live) != 1 || live[0].ID != sess.ID {
		t.Fatalf("after revoke-others live = %+v, want only kept session", live)
	}

	// RevokeSession removes the last one; a revoked token no longer resolves as live.
	if err := s.RevokeSession(ctx, sess.ID); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}
	live, _ = s.ListSessionsByUser(ctx, u.ID)
	if len(live) != 0 {
		t.Fatalf("after revoke live = %+v, want 0", live)
	}
	// The row still exists but carries revoked_at.
	revoked, err := s.GetSessionByTokenHash(ctx, "hash-A")
	if err != nil || revoked.RevokedAt == nil {
		t.Fatalf("revoked session lookup = (%+v,%v), want revoked_at set", revoked, err)
	}

	if _, err := s.GetSessionByTokenHash(ctx, "does-not-exist"); err != ErrNotFound {
		t.Fatalf("GetSessionByTokenHash(missing) = %v, want ErrNotFound", err)
	}
}

func TestAdvisoryLockRequiresTx(t *testing.T) {
	s := requireStore(t)
	if err := s.AdvisoryLock(context.Background(), 1); err == nil {
		t.Fatal("AdvisoryLock on a pooled store must error (xact lock needs a tx)")
	}
	// Inside WithTx it succeeds.
	err := s.WithTx(context.Background(), func(tx Store) error {
		return tx.AdvisoryLock(context.Background(), 1)
	})
	if err != nil {
		t.Fatalf("AdvisoryLock inside WithTx: %v", err)
	}
}

// bootstrapRaceLockKey mirrors auth.bootstrapLockKey (unexported there); only a
// value that is stable and shared across the racing goroutines matters here.
const bootstrapRaceLockKey int64 = 0x70726f7863 // "proxc"

// errRaceLost marks a goroutine that observed a user already existed under the
// lock and therefore correctly declined to create a second first-admin.
var errRaceLost = errors.New("race lost")

// TestBootstrapRaceGuard proves the first-admin creation guard that
// auth.createFirstAdmin relies on: under pg_advisory_xact_lock, re-checking
// CountUsers==0 before inserting serializes concurrent first-run attempts so
// exactly one wins. fakeStore's no-op AdvisoryLock cannot demonstrate this, so
// it must be exercised against real Postgres.
func TestBootstrapRaceGuard(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetIdentityTables(t, s)
	t.Cleanup(func() { resetIdentityTables(t, s) })

	const goroutines = 20
	var (
		wg        sync.WaitGroup
		successes int64
	)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			err := s.WithTx(ctx, func(tx Store) error {
				if err := tx.AdvisoryLock(ctx, bootstrapRaceLockKey); err != nil {
					return err
				}
				n, err := tx.CountUsers(ctx)
				if err != nil {
					return err
				}
				if n > 0 {
					return errRaceLost // another goroutine already created the first admin
				}
				_, err = tx.CreateUser(ctx, CreateUserParams{
					Email:           fmt.Sprintf("admin%d@example.com", i),
					DisplayName:     "Admin",
					PasswordHash:    "h",
					PasswordAlgo:    "argon2id",
					IsPlatformAdmin: true,
				})
				return err
			})
			switch {
			case err == nil:
				atomic.AddInt64(&successes, 1)
			case errors.Is(err, errRaceLost):
				// expected for the losers
			default:
				t.Errorf("goroutine %d: unexpected error: %v", i, err)
			}
		}(i)
	}
	wg.Wait()

	if successes != 1 {
		t.Fatalf("concurrent first-admin creation succeeded %d times, want exactly 1", successes)
	}
	if n, err := s.CountUsers(context.Background()); err != nil || n != 1 {
		t.Fatalf("users after race = (%d,%v), want (1,nil)", n, err)
	}
}
