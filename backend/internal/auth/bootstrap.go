package auth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// defaultTenantSlug is the tenant every first platform admin gets Owner on.
const defaultTenantSlug = "default"

// bootstrapLockKey is a fixed advisory-lock key that serializes first-user
// creation (bootstrap endpoint + env-admin seed) so two concurrent attempts
// cannot both observe zero users and both insert.
const bootstrapLockKey int64 = 0x70726f7863 // "proxc"

// errAlreadyBootstrapped signals that a user already exists, so the first-run
// path must refuse (mapped to 409 by the bootstrap handler).
var errAlreadyBootstrapped = errors.New("auth: already bootstrapped")

// createFirstAdmin creates the platform-admin user and an Owner membership on
// the default tenant, inside the caller's transaction. It re-checks CountUsers
// under an advisory lock so it is race-safe; it returns errAlreadyBootstrapped
// if any user already exists.
func createFirstAdmin(ctx context.Context, s store.Store, email, displayName, passwordHash, algo string) (*store.User, error) {
	if err := s.AdvisoryLock(ctx, bootstrapLockKey); err != nil {
		return nil, err
	}
	n, err := s.CountUsers(ctx)
	if err != nil {
		return nil, err
	}
	if n > 0 {
		return nil, errAlreadyBootstrapped
	}
	user, err := s.CreateUser(ctx, store.CreateUserParams{
		Email:           email,
		DisplayName:     displayName,
		PasswordHash:    passwordHash,
		PasswordAlgo:    algo,
		IsPlatformAdmin: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create admin user: %w", err)
	}
	tenant, err := s.GetTenantBySlug(ctx, defaultTenantSlug)
	if err != nil {
		return nil, fmt.Errorf("lookup default tenant: %w", err)
	}
	if _, err := s.CreateMembership(ctx, store.CreateMembershipParams{
		UserID:    user.ID,
		ScopeType: "tenant",
		ScopeID:   tenant.ID,
		Role:      "owner",
	}); err != nil {
		return nil, fmt.Errorf("grant owner membership: %w", err)
	}
	return user, nil
}

// SeedEnvAdmin performs the one-time env-admin cutover: when no users exist yet
// and ADMIN_USER + a password (hash or plaintext) are configured, it seeds a
// single platform-admin DB user (bcrypt hash, self-upgrades to Argon2id on
// first login) with an Owner membership on the default tenant. Idempotent — it
// only acts when the users table is empty. If users already exist while ADMIN_*
// is still set, it logs a WARN that the env admin is now inert.
//
// email = ADMIN_USER if it contains '@', else <ADMIN_USER>@proxcloud.local.
func SeedEnvAdmin(ctx context.Context, st store.Store, adminUser, adminPasswordHash, adminPassword string, log *slog.Logger) error {
	haveEnvAdmin := adminUser != "" && (adminPasswordHash != "" || adminPassword != "")

	n, err := st.CountUsers(ctx)
	if err != nil {
		return fmt.Errorf("auth: seed env admin: count users: %w", err)
	}
	if n > 0 {
		if adminUser != "" || adminPasswordHash != "" || adminPassword != "" {
			log.Warn("ADMIN_* is set but ignored; a platform admin already exists; remove ADMIN_* from the environment")
		}
		return nil
	}
	if !haveEnvAdmin {
		// Fresh install with no env admin — first-run bootstrap endpoint is the path.
		return nil
	}

	email := adminUser
	if !strings.Contains(email, "@") {
		email = email + "@proxcloud.local"
	}
	// bcrypt hash (validates ADMIN_PASSWORD_HASH or hashes ADMIN_PASSWORD at boot).
	hash, err := ResolveHash(adminPasswordHash, adminPassword)
	if err != nil {
		return fmt.Errorf("auth: seed env admin: %w", err)
	}

	err = st.WithTx(ctx, func(s store.Store) error {
		_, err := createFirstAdmin(ctx, s, email, "Administrator", hash, AlgoBcrypt)
		return err
	})
	if errors.Is(err, errAlreadyBootstrapped) {
		// Raced with another path that created the first user; treat as done.
		log.Warn("ADMIN_* is set but ignored; a platform admin already exists; remove ADMIN_* from the environment")
		return nil
	}
	if err != nil {
		return fmt.Errorf("auth: seed env admin: %w", err)
	}
	log.Warn("SEEDED PLATFORM ADMIN FROM ADMIN_* — sign in with this email, change the password in Settings, then remove ADMIN_* from the environment",
		"email", email)
	return nil
}
