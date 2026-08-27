//go:build integration

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// resetSecurityTables clears the Phase-5 aggregates and users so each test
// starts clean. invitations.invited_by references users with no cascade, so it
// must be cleared before users; login_challenges/totp_secrets/recovery_codes
// cascade on user delete but are cleared explicitly for isolation. Guarded
// against non-ephemeral databases (see guardDestructive).
func resetSecurityTables(t *testing.T, s *PgStore) {
	t.Helper()
	guardDestructive(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, tbl := range []string{"login_challenges", "recovery_codes", "totp_secrets", "invitations"} {
		if _, err := s.pool.Exec(ctx, "DELETE FROM "+tbl); err != nil {
			t.Fatalf("reset %s: %v", tbl, err)
		}
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM users`); err != nil {
		t.Fatalf("reset users: %v", err)
	}
}

func mustUser(t *testing.T, s *PgStore, email string) *User {
	t.Helper()
	u, err := s.CreateUser(context.Background(), CreateUserParams{
		Email: email, DisplayName: email, PasswordHash: "h", PasswordAlgo: "argon2id",
	})
	if err != nil {
		t.Fatalf("CreateUser(%s): %v", email, err)
	}
	return u
}

func TestInvitationLifecycle(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetSecurityTables(t, s)
	t.Cleanup(func() { resetSecurityTables(t, s) })
	ctx := context.Background()

	inviter := mustUser(t, s, "owner@example.com")
	tenant, err := s.GetTenantBySlug(ctx, "default")
	if err != nil {
		t.Fatalf("GetTenantBySlug: %v", err)
	}

	// Create inside a caller tx (the contract's supersede+insert atomicity).
	var inv *Invitation
	err = s.WithTx(ctx, func(tx Store) error {
		var e error
		inv, e = tx.CreateInvitation(ctx, CreateInvitationParams{
			TokenHash: "hash-1", Email: "invitee@example.com", ScopeType: "tenant",
			ScopeID: tenant.ID, Role: "contributor", InvitedBy: &inviter.ID,
			ExpiresAt: time.Now().Add(72 * time.Hour),
		})
		return e
	})
	if err != nil {
		t.Fatalf("CreateInvitation: %v", err)
	}
	if inv.ID == "" || inv.Role != "contributor" || inv.ScopeID != tenant.ID || inv.AcceptedAt != nil {
		t.Fatalf("CreateInvitation returned %+v", inv)
	}

	// Lookup by hash; unknown hash → ErrNotFound.
	got, err := s.GetInvitationByTokenHash(ctx, "hash-1")
	if err != nil || got.ID != inv.ID {
		t.Fatalf("GetInvitationByTokenHash = (%+v,%v)", got, err)
	}
	if _, err := s.GetInvitationByTokenHash(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("GetInvitationByTokenHash(missing) = %v, want ErrNotFound", err)
	}

	// Pending list includes it.
	pend, err := s.ListPendingInvitationsByScopes(ctx, "tenant", []string{tenant.ID})
	if err != nil || len(pend) != 1 || pend[0].ID != inv.ID {
		t.Fatalf("ListPendingInvitationsByScopes = (%+v,%v)", pend, err)
	}

	// Re-inviting the same (email, scope) supersedes the pending row: the old
	// token is deleted, only the new one remains pending.
	err = s.WithTx(ctx, func(tx Store) error {
		_, e := tx.CreateInvitation(ctx, CreateInvitationParams{
			TokenHash: "hash-2", Email: "INVITEE@example.com", ScopeType: "tenant",
			ScopeID: tenant.ID, Role: "reader", ExpiresAt: time.Now().Add(72 * time.Hour),
		})
		return e
	})
	if err != nil {
		t.Fatalf("CreateInvitation (supersede): %v", err)
	}
	if _, err := s.GetInvitationByTokenHash(ctx, "hash-1"); err != ErrNotFound {
		t.Fatalf("superseded invite still present: %v", err)
	}
	pend, _ = s.ListPendingInvitationsByScopes(ctx, "tenant", []string{tenant.ID})
	if len(pend) != 1 || pend[0].TokenHash != "hash-2" {
		t.Fatalf("after supersede pending = %+v, want single hash-2", pend)
	}

	// A duplicate token_hash (different email so no supersede) → ErrConflict.
	_, err = s.CreateInvitation(ctx, CreateInvitationParams{
		TokenHash: "hash-2", Email: "other@example.com", ScopeType: "tenant",
		ScopeID: tenant.ID, Role: "reader", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate token_hash err = %v, want ErrConflict", err)
	}

	// Accept is single-use: first wins, second (raced) loses cleanly.
	active := pend[0].ID
	ok, err := s.MarkInvitationAccepted(ctx, active)
	if err != nil || !ok {
		t.Fatalf("MarkInvitationAccepted = (%v,%v), want (true,nil)", ok, err)
	}
	ok, err = s.MarkInvitationAccepted(ctx, active)
	if err != nil || ok {
		t.Fatalf("MarkInvitationAccepted (second) = (%v,%v), want (false,nil)", ok, err)
	}
	// Accepted invites drop out of the pending list.
	pend, _ = s.ListPendingInvitationsByScopes(ctx, "tenant", []string{tenant.ID})
	if len(pend) != 0 {
		t.Fatalf("accepted invite still pending: %+v", pend)
	}

	// Revoke: delete then re-delete → ErrNotFound.
	if err := s.DeleteInvitation(ctx, active); err != nil {
		t.Fatalf("DeleteInvitation: %v", err)
	}
	if err := s.DeleteInvitation(ctx, active); err != ErrNotFound {
		t.Fatalf("DeleteInvitation (gone) = %v, want ErrNotFound", err)
	}

	// Empty scope list is a cheap empty result, not a query error.
	if out, err := s.ListPendingInvitationsByScopes(ctx, "tenant", nil); err != nil || len(out) != 0 {
		t.Fatalf("ListPendingInvitationsByScopes(nil) = (%+v,%v)", out, err)
	}
}

func TestTOTPSecretLifecycle(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetSecurityTables(t, s)
	t.Cleanup(func() { resetSecurityTables(t, s) })
	ctx := context.Background()

	u := mustUser(t, s, "totp@example.com")

	if _, err := s.GetTOTPSecret(ctx, u.ID); err != ErrNotFound {
		t.Fatalf("GetTOTPSecret(none) = %v, want ErrNotFound", err)
	}

	ct1 := []byte{0x01, 0x02, 0x03, 0x04}
	if err := s.UpsertTOTPSecret(ctx, u.ID, ct1); err != nil {
		t.Fatalf("UpsertTOTPSecret: %v", err)
	}
	sec, err := s.GetTOTPSecret(ctx, u.ID)
	if err != nil || string(sec.SecretEncrypted) != string(ct1) || sec.ConfirmedAt != nil {
		t.Fatalf("GetTOTPSecret = (%+v,%v), want ct1 unconfirmed", sec, err)
	}

	// Confirm once; a second confirm has nothing unconfirmed → ErrNotFound.
	if err := s.ConfirmTOTPSecret(ctx, u.ID); err != nil {
		t.Fatalf("ConfirmTOTPSecret: %v", err)
	}
	sec, _ = s.GetTOTPSecret(ctx, u.ID)
	if sec.ConfirmedAt == nil {
		t.Fatal("ConfirmTOTPSecret did not stamp confirmed_at")
	}
	if err := s.ConfirmTOTPSecret(ctx, u.ID); err != ErrNotFound {
		t.Fatalf("ConfirmTOTPSecret (again) = %v, want ErrNotFound", err)
	}

	// Re-enroll (upsert) overwrites the ciphertext AND resets confirmed_at.
	ct2 := []byte{0xaa, 0xbb}
	if err := s.UpsertTOTPSecret(ctx, u.ID, ct2); err != nil {
		t.Fatalf("UpsertTOTPSecret (re-enroll): %v", err)
	}
	sec, _ = s.GetTOTPSecret(ctx, u.ID)
	if string(sec.SecretEncrypted) != string(ct2) || sec.ConfirmedAt != nil {
		t.Fatalf("re-enroll = %+v, want ct2 unconfirmed", sec)
	}

	// Delete, then delete again (idempotent).
	if err := s.DeleteTOTPSecret(ctx, u.ID); err != nil {
		t.Fatalf("DeleteTOTPSecret: %v", err)
	}
	if _, err := s.GetTOTPSecret(ctx, u.ID); err != ErrNotFound {
		t.Fatalf("GetTOTPSecret after delete = %v, want ErrNotFound", err)
	}
	if err := s.DeleteTOTPSecret(ctx, u.ID); err != nil {
		t.Fatalf("DeleteTOTPSecret (idempotent) = %v, want nil", err)
	}
}

func TestRecoveryCodesLifecycle(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetSecurityTables(t, s)
	t.Cleanup(func() { resetSecurityTables(t, s) })
	ctx := context.Background()

	u := mustUser(t, s, "recovery@example.com")

	if n, err := s.CountUnusedRecoveryCodes(ctx, u.ID); err != nil || n != 0 {
		t.Fatalf("CountUnusedRecoveryCodes(empty) = (%d,%v)", n, err)
	}

	replace := func(hashes []string) {
		t.Helper()
		if err := s.WithTx(ctx, func(tx Store) error {
			return tx.ReplaceRecoveryCodes(ctx, u.ID, hashes)
		}); err != nil {
			t.Fatalf("ReplaceRecoveryCodes: %v", err)
		}
	}

	replace([]string{"h1", "h2", "h3"})
	if n, _ := s.CountUnusedRecoveryCodes(ctx, u.ID); n != 3 {
		t.Fatalf("count after replace = %d, want 3", n)
	}

	// Consume once; a reuse loses; an unknown code loses.
	if ok, err := s.ConsumeRecoveryCode(ctx, u.ID, "h1"); err != nil || !ok {
		t.Fatalf("ConsumeRecoveryCode(h1) = (%v,%v), want (true,nil)", ok, err)
	}
	if ok, _ := s.ConsumeRecoveryCode(ctx, u.ID, "h1"); ok {
		t.Fatal("ConsumeRecoveryCode(h1) reused — not single-use")
	}
	if ok, _ := s.ConsumeRecoveryCode(ctx, u.ID, "unknown"); ok {
		t.Fatal("ConsumeRecoveryCode(unknown) reported true")
	}
	if n, _ := s.CountUnusedRecoveryCodes(ctx, u.ID); n != 2 {
		t.Fatalf("count after one consume = %d, want 2", n)
	}

	// Regenerate replaces the whole set: old unused codes are gone.
	replace([]string{"h4", "h5"})
	if n, _ := s.CountUnusedRecoveryCodes(ctx, u.ID); n != 2 {
		t.Fatalf("count after regenerate = %d, want 2", n)
	}
	if ok, _ := s.ConsumeRecoveryCode(ctx, u.ID, "h2"); ok {
		t.Fatal("old code h2 still consumable after regenerate")
	}

	if err := s.DeleteRecoveryCodes(ctx, u.ID); err != nil {
		t.Fatalf("DeleteRecoveryCodes: %v", err)
	}
	if n, _ := s.CountUnusedRecoveryCodes(ctx, u.ID); n != 0 {
		t.Fatalf("count after delete = %d, want 0", n)
	}
}

func TestLoginChallengeLifecycle(t *testing.T) {
	s := requireStore(t)
	if _, err := s.RunMigrations(); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}
	resetSecurityTables(t, s)
	t.Cleanup(func() { resetSecurityTables(t, s) })
	ctx := context.Background()

	u := mustUser(t, s, "challenge@example.com")
	ip, ua := "203.0.113.9", "curl/8"

	ch, err := s.CreateLoginChallenge(ctx, CreateLoginChallengeParams{
		UserID: u.ID, TokenHash: "chash-1", ExpiresAt: time.Now().Add(5 * time.Minute),
		IP: &ip, UserAgent: &ua,
	})
	if err != nil {
		t.Fatalf("CreateLoginChallenge: %v", err)
	}
	if ch.ID == "" || ch.UserID != u.ID || ch.Attempts != 0 || ch.ConsumedAt != nil {
		t.Fatalf("CreateLoginChallenge returned %+v", ch)
	}

	// Lookup by hash; unknown → ErrNotFound.
	got, err := s.GetLoginChallengeByTokenHash(ctx, "chash-1")
	if err != nil || got.ID != ch.ID {
		t.Fatalf("GetLoginChallengeByTokenHash = (%+v,%v)", got, err)
	}
	if _, err := s.GetLoginChallengeByTokenHash(ctx, "nope"); err != ErrNotFound {
		t.Fatalf("GetLoginChallengeByTokenHash(missing) = %v, want ErrNotFound", err)
	}

	// A duplicate token_hash → ErrConflict.
	if _, err := s.CreateLoginChallenge(ctx, CreateLoginChallengeParams{
		UserID: u.ID, TokenHash: "chash-1", ExpiresAt: time.Now().Add(time.Minute),
	}); err == nil || !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate challenge token_hash err = %v, want ErrConflict", err)
	}

	// Success path is single-use.
	if ok, err := s.ConsumeLoginChallenge(ctx, ch.ID); err != nil || !ok {
		t.Fatalf("ConsumeLoginChallenge = (%v,%v), want (true,nil)", ok, err)
	}
	if ok, _ := s.ConsumeLoginChallenge(ctx, ch.ID); ok {
		t.Fatal("ConsumeLoginChallenge reused — not single-use")
	}

	// Failure path: attempts increments; at maxAttempts it self-consumes (locks).
	ch2, err := s.CreateLoginChallenge(ctx, CreateLoginChallengeParams{
		UserID: u.ID, TokenHash: "chash-2", ExpiresAt: time.Now().Add(5 * time.Minute),
	})
	if err != nil {
		t.Fatalf("CreateLoginChallenge 2: %v", err)
	}
	const maxAttempts = 3
	for i := 1; i <= maxAttempts; i++ {
		locked, err := s.RecordChallengeFailure(ctx, ch2.ID, maxAttempts)
		if err != nil {
			t.Fatalf("RecordChallengeFailure #%d: %v", i, err)
		}
		wantLocked := i >= maxAttempts
		if locked != wantLocked {
			t.Fatalf("RecordChallengeFailure #%d locked = %v, want %v", i, locked, wantLocked)
		}
	}
	// Locked ⇒ consumed: the success path can no longer consume it, and a further
	// failure reports locked=true.
	if ok, _ := s.ConsumeLoginChallenge(ctx, ch2.ID); ok {
		t.Fatal("locked challenge was still consumable")
	}
	if locked, err := s.RecordChallengeFailure(ctx, ch2.ID, maxAttempts); err != nil || !locked {
		t.Fatalf("RecordChallengeFailure (already locked) = (%v,%v), want (true,nil)", locked, err)
	}
}
