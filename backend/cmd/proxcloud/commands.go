package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/timkrebs9/proxcloud/backend/internal/auth"
	"github.com/timkrebs9/proxcloud/backend/internal/authz"
	"github.com/timkrebs9/proxcloud/backend/internal/config"
	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// --- proxcloud migrate --------------------------------------------------------

// migrator is the store subset the migrate command needs — just enough to apply
// the embedded migrations and report the resulting schema version. Keeping it
// narrow lets applyMigrations be unit-tested against the fake store.
type migrator interface {
	RunMigrations() (uint, error)
}

// runMigrate is the one-shot migrator service (ADR-0014 §4): load config, open
// the store, apply the embedded migrations, log the resulting schema version,
// and exit. It never starts the server. Returns 0 on success and non-zero on
// failure so the migrator compose service and deploy.sh gate the cutover on it.
func runMigrate(log *slog.Logger) int {
	cfg, err := config.Load()
	if err != nil {
		log.Error("migrate failed", "stage", "config", "err", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("migrate failed", "stage", "datastore", "err", err)
		return 1
	}
	defer st.Close()

	if _, err := applyMigrations(st, log); err != nil {
		log.Error("migrate failed", "stage", "migrations", "err", err)
		return 1
	}
	return 0
}

// applyMigrations runs the embedded migrations and logs the resulting schema
// version (no secrets). Returns the version so the caller/test can assert on it.
func applyMigrations(m migrator, log *slog.Logger) (uint, error) {
	version, err := m.RunMigrations()
	if err != nil {
		return version, err
	}
	log.Info("migrations applied", "version", version)
	return version, nil
}

// --- proxcloud seed-smoke -----------------------------------------------------

// Smoke fixture identity (ADR-0016 §4). The tenant and its single project share
// the slug "smoke"; the pool id follows the pc-<tenant>-<project> convention so
// it matches what a real project create would derive.
const (
	smokeTenantSlug  = "smoke"
	smokeTenantName  = "Smoke Test"
	smokeProjectSlug = "smoke"
	smokeProjectName = "Smoke"
	smokePoolID      = "pc-smoke-smoke"
)

// Tiny dedicated caps for the smoke project (ADR-0016 §4): small enough that a
// runaway smoke run can never starve real tenants, large enough for a single
// throwaway LXC in the reserved VMID range. The smoke test must stay within
// these; widen here (and note it) if the smoke template needs more.
var (
	smokeQuotaVCPU   = 4
	smokeQuotaRAMMB  = int64(4096) // 4 GiB
	smokeQuotaDiskGB = int64(32)
	smokeQuotaCount  = 4
)

// passwordHasher is the hashing subset seedSmoke needs (satisfied by
// *auth.PasswordHasher), so the seed can be tested with a trivial fake hasher.
type passwordHasher interface {
	Hash(pw string) (string, error)
}

// smokeSeedResult records what a seed run created versus skipped — the basis for
// the idempotency test and the human-readable log line.
type smokeSeedResult struct {
	TenantID          string
	ProjectID         string
	UserID            string
	CreatedTenant     bool
	CreatedProject    bool
	CreatedUser       bool
	CreatedMembership bool
	CreatedQuota      bool
}

// runSeedSmoke provisions the idempotent, least-privilege smoke fixture
// (ADR-0016): a dedicated "smoke" tenant, a "smoke" project in it, and a smoke
// user holding a Contributor (never Owner, never platform-admin) tenant-scope
// membership, TOTP disabled. Credentials come from SMOKE_EMAIL / SMOKE_PASSWORD;
// the password is Argon2id-hashed with the production hasher. Safe to run on
// every staging deploy: it creates only what is absent and never touches other
// tenants or users. Returns 0 on success, non-zero on failure or missing env.
func runSeedSmoke(log *slog.Logger) int {
	email := strings.TrimSpace(os.Getenv("SMOKE_EMAIL"))
	password := os.Getenv("SMOKE_PASSWORD")
	if email == "" || password == "" {
		log.Error("seed-smoke failed: SMOKE_EMAIL and SMOKE_PASSWORD must both be set")
		return 1
	}

	cfg, err := config.Load()
	if err != nil {
		log.Error("seed-smoke failed", "stage", "config", "err", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("seed-smoke failed", "stage", "datastore", "err", err)
		return 1
	}
	defer st.Close()

	if _, err := seedSmoke(ctx, st, auth.NewHasher(), email, password, log); err != nil {
		log.Error("seed-smoke failed", "err", err)
		return 1
	}
	return 0
}

// seedSmoke is the idempotent core, factored out so it runs against the fake
// store in tests. Each step looks up by slug/email/scope and only creates when
// absent; the password is never logged. The membership is Contributor at the
// tenant scope, i.e. least privilege confined to the single-project smoke
// tenant (the 404 tenant boundary keeps it from seeing anything else).
func seedSmoke(ctx context.Context, st store.Store, hasher passwordHasher, email, password string, log *slog.Logger) (smokeSeedResult, error) {
	var res smokeSeedResult

	// 1. Tenant.
	tenant, created, err := ensureSmokeTenant(ctx, st)
	if err != nil {
		return res, fmt.Errorf("ensure smoke tenant: %w", err)
	}
	res.TenantID, res.CreatedTenant = tenant.ID, created

	// 2. Project in the tenant (its pool id is recorded, but no Proxmox pool is
	// created here — ADR-0016 leaves the PVE pool/template/storage to Tim).
	project, created, err := ensureSmokeProject(ctx, st, tenant.ID)
	if err != nil {
		return res, fmt.Errorf("ensure smoke project: %w", err)
	}
	res.ProjectID, res.CreatedProject = project.ID, created

	// 3. User. TOTP stays disabled and is_platform_admin false (CreateUser
	// defaults), so the smoke user is a plain identity.
	user, created, err := ensureSmokeUser(ctx, st, hasher, email, password)
	if err != nil {
		return res, fmt.Errorf("ensure smoke user: %w", err)
	}
	res.UserID, res.CreatedUser = user.ID, created

	// 4. Contributor membership on the smoke tenant (never Owner).
	created, err = ensureSmokeMembership(ctx, st, user.ID, tenant.ID)
	if err != nil {
		return res, fmt.Errorf("ensure smoke membership: %w", err)
	}
	res.CreatedMembership = created

	// 5. Tiny project quota so a runaway smoke run cannot starve real tenants.
	created, err = ensureSmokeQuota(ctx, st, project.ID)
	if err != nil {
		return res, fmt.Errorf("ensure smoke quota: %w", err)
	}
	res.CreatedQuota = created

	log.Info("seed-smoke complete",
		"email", email,
		"tenantSlug", smokeTenantSlug,
		"projectSlug", smokeProjectSlug,
		"role", authz.RoleContributorStr,
		"scope", "tenant",
		"createdTenant", res.CreatedTenant,
		"createdProject", res.CreatedProject,
		"createdUser", res.CreatedUser,
		"createdMembership", res.CreatedMembership,
		"createdQuota", res.CreatedQuota,
	)
	return res, nil
}

// ensureSmokeTenant returns the smoke tenant, creating it only if absent. A
// concurrent create that wins the UNIQUE(slug) race is folded back to a lookup.
func ensureSmokeTenant(ctx context.Context, st store.Store) (*store.Tenant, bool, error) {
	t, err := st.GetTenantBySlug(ctx, smokeTenantSlug)
	if err == nil {
		return t, false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}
	t, err = st.CreateTenant(ctx, store.CreateTenantParams{Name: smokeTenantName, Slug: smokeTenantSlug})
	if errors.Is(err, store.ErrConflict) {
		t, err = st.GetTenantBySlug(ctx, smokeTenantSlug)
		return t, false, err
	}
	if err != nil {
		return nil, false, err
	}
	return t, true, nil
}

// ensureSmokeProject returns the smoke project within the tenant, creating it
// only if absent (matched by slug within the tenant).
func ensureSmokeProject(ctx context.Context, st store.Store, tenantID string) (*store.Project, bool, error) {
	projects, err := st.ListProjectsByTenant(ctx, tenantID)
	if err != nil {
		return nil, false, err
	}
	for i := range projects {
		if projects[i].Slug == smokeProjectSlug {
			p := projects[i]
			return &p, false, nil
		}
	}
	p, err := st.CreateProject(ctx, store.CreateProjectParams{
		TenantID: tenantID,
		Name:     smokeProjectName,
		Slug:     smokeProjectSlug,
		PoolID:   smokePoolID,
	})
	if errors.Is(err, store.ErrConflict) {
		// Raced (or the pool id already exists): re-list and return the row.
		projects, lerr := st.ListProjectsByTenant(ctx, tenantID)
		if lerr != nil {
			return nil, false, lerr
		}
		for i := range projects {
			if projects[i].Slug == smokeProjectSlug {
				p := projects[i]
				return &p, false, nil
			}
		}
		return nil, false, err
	}
	if err != nil {
		return nil, false, err
	}
	return p, true, nil
}

// ensureSmokeUser returns the smoke user, creating it only if absent. On create
// the password is Argon2id-hashed; TOTP and platform-admin default off.
func ensureSmokeUser(ctx context.Context, st store.Store, hasher passwordHasher, email, password string) (*store.User, bool, error) {
	u, err := st.GetUserByEmail(ctx, email)
	if err == nil {
		return u, false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, false, err
	}
	hash, err := hasher.Hash(password)
	if err != nil {
		return nil, false, fmt.Errorf("hash password: %w", err)
	}
	u, err = st.CreateUser(ctx, store.CreateUserParams{
		Email:           email,
		DisplayName:     "Smoke Test",
		PasswordHash:    hash,
		PasswordAlgo:    auth.AlgoArgon2id,
		IsPlatformAdmin: false,
	})
	if errors.Is(err, store.ErrConflict) {
		u, err = st.GetUserByEmail(ctx, email)
		return u, false, err
	}
	if err != nil {
		return nil, false, err
	}
	return u, true, nil
}

// ensureSmokeMembership grants the user Contributor on the tenant scope if they
// do not already hold that grant. It never grants Owner and never widens an
// existing grant.
func ensureSmokeMembership(ctx context.Context, st store.Store, userID, tenantID string) (bool, error) {
	memberships, err := st.ListMembershipsByUser(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, m := range memberships {
		if m.ScopeType == "tenant" && m.ScopeID == tenantID && m.Role == authz.RoleContributorStr {
			return false, nil
		}
	}
	if _, err := st.CreateMembership(ctx, store.CreateMembershipParams{
		UserID:    userID,
		ScopeType: "tenant",
		ScopeID:   tenantID,
		Role:      authz.RoleContributorStr,
	}); err != nil {
		return false, err
	}
	return true, nil
}

// ensureSmokeQuota sets the tiny project-scoped caps if no quota row exists yet.
// It reports whether it created one (an existing row is left untouched so a
// manually tuned cap is never clobbered on redeploy).
func ensureSmokeQuota(ctx context.Context, st store.Store, projectID string) (bool, error) {
	_, err := st.GetQuota(ctx, "project", projectID)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	vcpu, ram, disk, count := smokeQuotaVCPU, smokeQuotaRAMMB, smokeQuotaDiskGB, smokeQuotaCount
	if _, err := st.UpsertQuota(ctx, store.UpsertQuotaParams{
		ScopeType: "project",
		ScopeID:   projectID,
		MaxVCPU:   &vcpu,
		MaxRAMMB:  &ram,
		MaxDiskGB: &disk,
		MaxCount:  &count,
	}); err != nil {
		return false, err
	}
	return true, nil
}
