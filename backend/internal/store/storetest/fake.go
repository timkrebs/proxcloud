// Package storetest provides an in-memory store.Store for table-driven tenancy
// and authz tests — the store-side mirror of proxmoxtest.MockClient. It is a
// faithful behavioral double (tenant/project filters, ownership status,
// unique-VMID reservation) so tests exercise the real enforcement chain and
// handlers against realistic data without a Postgres.
package storetest

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// Fake is an in-memory store.Store. Construct with New and seed via the Add*
// helpers; all methods are mutex-guarded for data-race-free concurrent use.
type Fake struct {
	mu   sync.Mutex
	seq  int
	Now  func() time.Time
	fail map[string]error // method name -> forced error (for error-path tests)

	users       map[string]*store.User
	sessions    map[string]*store.Session
	memberships map[string]*store.Membership
	tenants     map[string]*store.Tenant         // by id
	projects    map[string]*store.Project        // by id
	ownership   map[int]*store.ResourceOwnership // by vmid
	quotas      map[string]*store.Quota          // by scopeType+"|"+scopeID
	audit       []*store.AuditEntry              // append-only; newest last

	// Phase 5 (ADR-0013) aggregates.
	invitations map[string]*store.Invitation // by id
	totp        map[string]*store.TOTPSecret // by userID
	recovery    []*fakeRecoveryCode          // append-only; single-use via UsedAt
	challenges  map[string]*fakeChallenge    // by id
	chalByHash  map[string]string            // token_hash -> challenge id

	// Scheduler (ADR-0018) job store.
	jobs map[string]*store.Job // by id

	// Auto-shutdown schedules (ADR-0019).
	schedules map[string]*store.Schedule // by id

	// TTL / ephemeral resources (ADR-0020).
	ttls        map[int]*store.TTL                 // by vmid
	ttlPolicies map[string]*store.ProjectTTLPolicy // by tenantID|projectID

	// Deployment sets (ADR-0029).
	deploymentSets map[string]*store.DeploymentSet // by id
}

// fakeRecoveryCode is one stored recovery code (hashed, single-use).
type fakeRecoveryCode struct {
	UserID   string
	CodeHash string
	UsedAt   *time.Time
}

// fakeChallenge wraps a LoginChallenge with the token hash the domain struct
// deliberately omits (mirrors sessions storing only the hash).
type fakeChallenge struct {
	c         store.LoginChallenge
	tokenHash string
}

var _ store.Store = (*Fake)(nil)

// New returns an empty Fake.
func New() *Fake {
	return &Fake{
		Now:            time.Now,
		fail:           map[string]error{},
		users:          map[string]*store.User{},
		sessions:       map[string]*store.Session{},
		memberships:    map[string]*store.Membership{},
		tenants:        map[string]*store.Tenant{},
		projects:       map[string]*store.Project{},
		ownership:      map[int]*store.ResourceOwnership{},
		quotas:         map[string]*store.Quota{},
		audit:          []*store.AuditEntry{},
		invitations:    map[string]*store.Invitation{},
		totp:           map[string]*store.TOTPSecret{},
		recovery:       []*fakeRecoveryCode{},
		challenges:     map[string]*fakeChallenge{},
		chalByHash:     map[string]string{},
		jobs:           map[string]*store.Job{},
		schedules:      map[string]*store.Schedule{},
		ttls:           map[int]*store.TTL{},
		ttlPolicies:    map[string]*store.ProjectTTLPolicy{},
		deploymentSets: map[string]*store.DeploymentSet{},
	}
}

func (f *Fake) next(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%d", prefix, f.seq)
}

// FailOn forces method (by name) to return err on its next calls (until cleared
// by FailOn(method, nil)). Used for store error-path coverage.
func (f *Fake) FailOn(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.fail, method)
		return
	}
	f.fail[method] = err
}

func (f *Fake) failed(method string) error { return f.fail[method] }

// --- seed helpers (exported; take the lock) ---

// AddUser seeds a user and returns its id.
func (f *Fake) AddUser(email, displayName string, admin bool) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next("user")
	f.users[id] = &store.User{ID: id, Email: email, DisplayName: displayName, IsPlatformAdmin: admin, CreatedAt: f.Now(), UpdatedAt: f.Now()}
	return id
}

// AddTenant seeds a tenant and returns its id.
func (f *Fake) AddTenant(name, slug string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next("tenant")
	f.tenants[id] = &store.Tenant{ID: id, Name: name, Slug: slug, CreatedAt: f.Now(), UpdatedAt: f.Now()}
	return id
}

// AddProject seeds a project and returns its id.
func (f *Fake) AddProject(tenantID, name, slug, poolID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next("proj")
	f.projects[id] = &store.Project{ID: id, TenantID: tenantID, Name: name, Slug: slug, PoolID: poolID, CreatedAt: f.Now(), UpdatedAt: f.Now()}
	return id
}

// AddMembership seeds a role grant at a scope.
func (f *Fake) AddMembership(userID, scopeType, scopeID, role string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next("mem")
	f.memberships[id] = &store.Membership{ID: id, UserID: userID, ScopeType: scopeType, ScopeID: scopeID, Role: role, CreatedAt: f.Now(), UpdatedAt: f.Now()}
}

// AddOwnership seeds an ownership row and returns its id.
func (f *Fake) AddOwnership(tenantID, projectID string, vmid int, guestType, node, status string, createdBy *string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next("own")
	f.ownership[vmid] = &store.ResourceOwnership{ID: id, TenantID: tenantID, ProjectID: projectID, VMID: vmid, GuestType: guestType, Node: node, Status: status, CreatedBy: createdBy, CreatedAt: f.Now(), UpdatedAt: f.Now()}
	return id
}

// AddPendingReservation seeds a pending ownership row carrying a reserved
// allocation (what ReserveOwnership would have inserted) and returns its id — a
// convenience for ComputeUsage/quota tests.
func (f *Fake) AddPendingReservation(tenantID, projectID string, vmid int, guestType, node string, vcpu int, ramMB, diskGB int64) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next("own")
	rv, rr, rd := vcpu, ramMB, diskGB
	f.ownership[vmid] = &store.ResourceOwnership{
		ID: id, TenantID: tenantID, ProjectID: projectID, VMID: vmid, GuestType: guestType,
		Node: node, Status: "pending", ReservedVCPU: &rv, ReservedRAMMB: &rr, ReservedDiskGB: &rd,
		CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	return id
}

// AddQuota seeds (or replaces) a scope's stored limits. Nil pointers leave that
// dimension unlimited.
func (f *Fake) AddQuota(scopeType, scopeID string, maxVCPU *int, maxRAMMB, maxDiskGB *int64, maxCount *int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.quotas[scopeType+"|"+scopeID] = &store.Quota{
		ID: f.next("quota"), ScopeType: scopeType, ScopeID: scopeID,
		MaxVCPU: maxVCPU, MaxRAMMB: maxRAMMB, MaxDiskGB: maxDiskGB, MaxCount: maxCount,
		CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
}

// AddSession seeds a live session for userID with the given raw token and active
// tenant, and returns the session id.
func (f *Fake) AddSession(userID, tokenHash string, activeTenant *string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next("sess")
	now := f.Now()
	f.sessions[id] = &store.Session{
		ID: id, TokenHash: tokenHash, UserID: userID, ActiveTenantID: activeTenant,
		CreatedAt: now, LastSeenAt: now, AbsoluteExpiresAt: now.Add(24 * time.Hour),
	}
	return id
}

// OwnershipStatus returns the current status of the ownership row for vmid, or
// "" if none — a convenience for finalize/release assertions.
func (f *Fake) OwnershipStatus(vmid int) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if o, ok := f.ownership[vmid]; ok {
		return o.Status
	}
	return ""
}

// --- base ---

func (f *Fake) Ping(context.Context) error                { return nil }
func (f *Fake) RunMigrations() (uint, error)              { return 3, nil }
func (f *Fake) AdvisoryLock(context.Context, int64) error { return nil }

// WithTx snapshots the aggregates the Create* helpers mutate (additively) and
// restores them if fn returns an error, giving the fake real commit-or-rollback
// semantics. That lets tests assert atomicity (e.g. a project failure inside the
// tenant-creation tx leaves NO tenant row). fn runs against the same Fake, so
// nested WithTx (reentrant in the real store) simply re-snapshots.
func (f *Fake) WithTx(ctx context.Context, fn func(store.Store) error) error {
	f.mu.Lock()
	tenants := make(map[string]*store.Tenant, len(f.tenants))
	for k, v := range f.tenants {
		tenants[k] = v
	}
	projects := make(map[string]*store.Project, len(f.projects))
	for k, v := range f.projects {
		projects[k] = v
	}
	ownership := make(map[int]*store.ResourceOwnership, len(f.ownership))
	for k, v := range f.ownership {
		ownership[k] = v
	}
	memberships := make(map[string]*store.Membership, len(f.memberships))
	for k, v := range f.memberships {
		memberships[k] = v
	}
	invitations := make(map[string]*store.Invitation, len(f.invitations))
	for k, v := range f.invitations {
		invitations[k] = v
	}
	totp := make(map[string]*store.TOTPSecret, len(f.totp))
	for k, v := range f.totp {
		totp[k] = v
	}
	recovery := append([]*fakeRecoveryCode(nil), f.recovery...)
	challenges := make(map[string]*fakeChallenge, len(f.challenges))
	for k, v := range f.challenges {
		challenges[k] = v
	}
	chalByHash := make(map[string]string, len(f.chalByHash))
	for k, v := range f.chalByHash {
		chalByHash[k] = v
	}
	jobs := make(map[string]*store.Job, len(f.jobs))
	for k, v := range f.jobs {
		jobs[k] = v
	}
	schedules := make(map[string]*store.Schedule, len(f.schedules))
	for k, v := range f.schedules {
		schedules[k] = v
	}
	ttls := make(map[int]*store.TTL, len(f.ttls))
	for k, v := range f.ttls {
		ttls[k] = v
	}
	ttlPolicies := make(map[string]*store.ProjectTTLPolicy, len(f.ttlPolicies))
	for k, v := range f.ttlPolicies {
		ttlPolicies[k] = v
	}
	deploymentSets := make(map[string]*store.DeploymentSet, len(f.deploymentSets))
	for k, v := range f.deploymentSets {
		deploymentSets[k] = v
	}
	f.mu.Unlock()

	if err := fn(f); err != nil {
		f.mu.Lock()
		f.tenants = tenants
		f.projects = projects
		f.ownership = ownership
		f.memberships = memberships
		f.invitations = invitations
		f.totp = totp
		f.recovery = recovery
		f.challenges = challenges
		f.chalByHash = chalByHash
		f.jobs = jobs
		f.schedules = schedules
		f.ttls = ttls
		f.ttlPolicies = ttlPolicies
		f.deploymentSets = deploymentSets
		f.mu.Unlock()
		return err
	}
	return nil
}

func (f *Fake) GetTenantBySlug(_ context.Context, slug string) (*store.Tenant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tenants {
		if t.Slug == slug {
			c := *t
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *Fake) ListTenants(context.Context) ([]store.Tenant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Tenant{}
	for _, t := range f.tenants {
		out = append(out, *t)
	}
	return out, nil
}

func (f *Fake) GetProjectByPoolID(_ context.Context, poolID string) (*store.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, p := range f.projects {
		if p.PoolID == poolID {
			c := *p
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *Fake) ListProjectsByTenant(_ context.Context, tenantID string) ([]store.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Project{}
	for _, p := range f.projects {
		if p.TenantID == tenantID {
			out = append(out, *p)
		}
	}
	return out, nil
}

// --- users ---

func (f *Fake) CreateUser(_ context.Context, p store.CreateUserParams) (*store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("CreateUser"); err != nil {
		return nil, err
	}
	// Mirror UNIQUE(lower(email)): a duplicate email is a conflict (so the
	// invite-accept path can map a raced create to a 409, not a 500).
	for _, u := range f.users {
		if strings.EqualFold(u.Email, p.Email) {
			return nil, fmt.Errorf("create user: %w", store.ErrConflict)
		}
	}
	id := f.next("user")
	u := &store.User{ID: id, Email: p.Email, DisplayName: p.DisplayName, IsPlatformAdmin: p.IsPlatformAdmin, CreatedAt: f.Now(), UpdatedAt: f.Now()}
	f.users[id] = u
	c := *u
	return &c, nil
}

func (f *Fake) GetUserByEmail(_ context.Context, email string) (*store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if strings.EqualFold(u.Email, email) {
			c := *u
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *Fake) GetUserByID(_ context.Context, id string) (*store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[id]; ok {
		c := *u
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) CountUsers(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.users), nil
}

func (f *Fake) UpdatePasswordHash(_ context.Context, userID, hash, algo string) error {
	return nil
}
func (f *Fake) SetTOTPEnabled(context.Context, string, bool) error { return nil }

func (f *Fake) ListUsersByIDs(_ context.Context, ids []string) (map[string]store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]store.User{}
	for _, id := range ids {
		if u, ok := f.users[id]; ok {
			out[id] = *u
		}
	}
	return out, nil
}

// --- sessions ---

func (f *Fake) CreateSession(_ context.Context, p store.CreateSessionParams) (*store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next("sess")
	now := f.Now()
	s := &store.Session{ID: id, TokenHash: p.TokenHash, UserID: p.UserID, CreatedAt: now, LastSeenAt: now, AbsoluteExpiresAt: p.AbsoluteExpiresAt, IP: p.IP, UserAgent: p.UserAgent}
	f.sessions[id] = s
	c := *s
	return &c, nil
}

func (f *Fake) GetSessionByTokenHash(_ context.Context, tokenHash string) (*store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.TokenHash == tokenHash {
			c := *s
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *Fake) TouchSession(_ context.Context, sessionID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[sessionID]; ok {
		s.LastSeenAt = at
	}
	return nil
}

func (f *Fake) SetSessionActiveTenant(_ context.Context, sessionID string, tenantID *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok {
		return store.ErrNotFound
	}
	s.ActiveTenantID = tenantID
	return nil
}

func (f *Fake) RevokeSession(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[sessionID]; ok && s.RevokedAt == nil {
		t := f.Now()
		s.RevokedAt = &t
	}
	return nil
}

func (f *Fake) RevokeOtherUserSessions(_ context.Context, userID, keep string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.UserID == userID && s.ID != keep && s.RevokedAt == nil {
			t := f.Now()
			s.RevokedAt = &t
		}
	}
	return nil
}

func (f *Fake) ListSessionsByUser(_ context.Context, userID string) ([]store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.Now()
	out := []store.Session{}
	for _, s := range f.sessions {
		if s.UserID == userID && s.RevokedAt == nil && s.AbsoluteExpiresAt.After(now) {
			out = append(out, *s)
		}
	}
	return out, nil
}

// --- memberships ---

func (f *Fake) CreateMembership(_ context.Context, p store.CreateMembershipParams) (*store.Membership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next("mem")
	m := &store.Membership{ID: id, UserID: p.UserID, ScopeType: p.ScopeType, ScopeID: p.ScopeID, Role: p.Role, CreatedAt: f.Now(), UpdatedAt: f.Now()}
	f.memberships[id] = m
	c := *m
	return &c, nil
}

func (f *Fake) ListMembershipsByUser(_ context.Context, userID string) ([]store.Membership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Membership{}
	for _, m := range f.memberships {
		if m.UserID == userID {
			out = append(out, *m)
		}
	}
	return out, nil
}

func (f *Fake) ListMembershipsByScope(_ context.Context, scopeType, scopeID string) ([]store.Membership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.Membership{}
	for _, m := range f.memberships {
		if m.ScopeType == scopeType && m.ScopeID == scopeID {
			out = append(out, *m)
		}
	}
	return out, nil
}

func (f *Fake) ListMembershipsByScopes(_ context.Context, scopeType string, scopeIDs []string) ([]store.Membership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ListMembershipsByScopes"); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range scopeIDs {
		want[id] = true
	}
	out := []store.Membership{}
	for _, m := range f.memberships {
		if m.ScopeType == scopeType && want[m.ScopeID] {
			out = append(out, *m)
		}
	}
	return out, nil
}

func (f *Fake) GetEffectiveRoles(_ context.Context, userID, tenantID string) (string, map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("GetEffectiveRoles"); err != nil {
		return "", nil, err
	}
	tenantRole := ""
	projectRoles := map[string]string{}
	for _, m := range f.memberships {
		if m.UserID != userID {
			continue
		}
		switch m.ScopeType {
		case "tenant":
			if m.ScopeID == tenantID && rank(m.Role) > rank(tenantRole) {
				tenantRole = m.Role
			}
		case "project":
			if p, ok := f.projects[m.ScopeID]; ok && p.TenantID == tenantID && rank(m.Role) > rank(projectRoles[m.ScopeID]) {
				projectRoles[m.ScopeID] = m.Role
			}
		}
	}
	return tenantRole, projectRoles, nil
}

// --- tenants ---

func (f *Fake) CreateTenant(_ context.Context, p store.CreateTenantParams) (*store.Tenant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Mirror the UNIQUE(slug) constraint: a duplicate slug is a conflict.
	for _, t := range f.tenants {
		if t.Slug == p.Slug {
			return nil, fmt.Errorf("create tenant: %w", store.ErrConflict)
		}
	}
	id := f.next("tenant")
	t := &store.Tenant{ID: id, Name: p.Name, Slug: p.Slug, CreatedAt: f.Now(), UpdatedAt: f.Now()}
	f.tenants[id] = t
	c := *t
	return &c, nil
}

func (f *Fake) GetTenantByID(_ context.Context, id string) (*store.Tenant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tenants[id]; ok {
		c := *t
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) ListTenantsForUser(_ context.Context, userID string) ([]store.TenantWithRole, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	roleByTenant := map[string]string{}
	for _, m := range f.memberships {
		if m.UserID != userID {
			continue
		}
		var tid string
		switch m.ScopeType {
		case "tenant":
			tid = m.ScopeID
		case "project":
			if p, ok := f.projects[m.ScopeID]; ok {
				tid = p.TenantID
			}
		}
		if tid != "" && rank(m.Role) > rank(roleByTenant[tid]) {
			roleByTenant[tid] = m.Role
		}
	}
	out := []store.TenantWithRole{}
	for _, t := range f.tenants {
		if role, ok := roleByTenant[t.ID]; ok {
			out = append(out, store.TenantWithRole{Tenant: *t, Role: role})
		}
	}
	return out, nil
}

// --- projects ---

func (f *Fake) CreateProject(_ context.Context, p store.CreateProjectParams) (*store.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Mirror UNIQUE(tenant_id, slug) and the migration-000002 UNIQUE(pool_id):
	// a duplicate slug within the tenant, or any duplicate pool id, is a conflict.
	for _, ex := range f.projects {
		if ex.PoolID == p.PoolID || (ex.TenantID == p.TenantID && ex.Slug == p.Slug) {
			return nil, fmt.Errorf("create project: %w", store.ErrConflict)
		}
	}
	id := f.next("proj")
	proj := &store.Project{ID: id, TenantID: p.TenantID, Name: p.Name, Slug: p.Slug, PoolID: p.PoolID, CreatedAt: f.Now(), UpdatedAt: f.Now()}
	f.projects[id] = proj
	c := *proj
	return &c, nil
}

func (f *Fake) GetProjectByID(_ context.Context, id string) (*store.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.projects[id]; ok {
		c := *p
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) RenameProject(_ context.Context, id, name string) (*store.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	p.Name = name
	p.UpdatedAt = f.Now()
	c := *p
	return &c, nil
}

func (f *Fake) DeleteProject(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.projects, id)
	return nil
}

func (f *Fake) CountActiveOwnershipByProject(_ context.Context, projectID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, o := range f.ownership {
		if o.ProjectID == projectID && (o.Status == "active" || o.Status == "pending") {
			n++
		}
	}
	return n, nil
}

// --- ownership ---

func (f *Fake) GetOwnershipByVMID(_ context.Context, vmid int) (*store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if o, ok := f.ownership[vmid]; ok {
		c := *o
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) CreateOwnership(_ context.Context, p store.CreateOwnershipParams) (*store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	// Mirror the postgres ON CONFLICT (vmid) revive: a live (active/pending) row
	// is a conflict, but a tombstoned VMID is free — revive it in place (same id).
	if existing, ok := f.ownership[p.VMID]; ok {
		if existing.Status != "tombstoned" {
			return nil, fmt.Errorf("create ownership for vmid %d: %w", p.VMID, store.ErrConflict)
		}
		existing.TenantID = p.TenantID
		existing.ProjectID = p.ProjectID
		existing.GuestType = p.GuestType
		existing.Node = p.Node
		existing.CreatedBy = p.CreatedBy
		existing.Status = p.Status
		existing.PVEUPID = nil
		existing.ReservedVCPU, existing.ReservedRAMMB, existing.ReservedDiskGB = p.ReservedVCPU, p.ReservedRAMMB, p.ReservedDiskGB
		existing.DeploymentSetID, existing.Role = p.DeploymentSetID, p.Role
		existing.UpdatedAt = f.Now()
		c := *existing
		return &c, nil
	}
	id := f.next("own")
	o := &store.ResourceOwnership{ID: id, TenantID: p.TenantID, ProjectID: p.ProjectID, VMID: p.VMID, GuestType: p.GuestType, Node: p.Node, CreatedBy: p.CreatedBy, Status: p.Status,
		ReservedVCPU: p.ReservedVCPU, ReservedRAMMB: p.ReservedRAMMB, ReservedDiskGB: p.ReservedDiskGB,
		DeploymentSetID: p.DeploymentSetID, Role: p.Role, CreatedAt: f.Now(), UpdatedAt: f.Now()}
	f.ownership[p.VMID] = o
	c := *o
	return &c, nil
}

func (f *Fake) FinalizeOwnership(_ context.Context, id, upid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.ownership {
		if o.ID == id && o.Status == "pending" {
			o.Status = "active"
			u := upid
			o.PVEUPID = &u
			o.UpdatedAt = f.Now()
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *Fake) ReleaseOwnership(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for vmid, o := range f.ownership {
		if o.ID == id && o.Status == "pending" {
			delete(f.ownership, vmid)
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *Fake) TombstoneOwnership(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.ownership {
		if o.ID == id {
			o.Status = "tombstoned"
			o.UpdatedAt = f.Now()
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *Fake) SetAutoStopped(_ context.Context, vmid int, v bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("SetAutoStopped"); err != nil {
		return err
	}
	o, ok := f.ownership[vmid]
	if !ok {
		return store.ErrNotFound
	}
	o.AutoStopped = v
	o.UpdatedAt = f.Now()
	return nil
}

func (f *Fake) SetExpiredAt(_ context.Context, vmid int, at *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("SetExpiredAt"); err != nil {
		return err
	}
	o, ok := f.ownership[vmid]
	if !ok {
		return store.ErrNotFound
	}
	o.ExpiredAt = at
	o.UpdatedAt = f.Now()
	return nil
}

func (f *Fake) ListOwnershipByTenant(_ context.Context, tenantID string) ([]store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ListOwnershipByTenant"); err != nil {
		return nil, err
	}
	out := []store.ResourceOwnership{}
	for _, o := range f.ownership {
		if o.TenantID == tenantID {
			out = append(out, *o)
		}
	}
	return out, nil
}

func (f *Fake) ListOwnershipByProject(_ context.Context, projectID string) ([]store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.ResourceOwnership{}
	for _, o := range f.ownership {
		if o.ProjectID == projectID {
			out = append(out, *o)
		}
	}
	return out, nil
}

func (f *Fake) ListActiveVMIDs(context.Context) (map[int]bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int]bool{}
	for vmid, o := range f.ownership {
		if o.Status == "active" || o.Status == "pending" {
			out[vmid] = true
		}
	}
	return out, nil
}

func (f *Fake) ListStalePendingOwnership(_ context.Context, olderThan time.Time) ([]store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ListStalePendingOwnership"); err != nil {
		return nil, err
	}
	out := []store.ResourceOwnership{}
	for _, o := range f.ownership {
		if o.Status == "pending" && o.CreatedAt.Before(olderThan) {
			out = append(out, *o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// --- quotas + reservation + audit (Phase 4) ---

func (f *Fake) GetQuota(_ context.Context, scopeType, scopeID string) (*store.Quota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("GetQuota"); err != nil {
		return nil, err
	}
	if q, ok := f.quotas[scopeType+"|"+scopeID]; ok {
		c := *q
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) UpsertQuota(_ context.Context, p store.UpsertQuotaParams) (*store.Quota, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("UpsertQuota"); err != nil {
		return nil, err
	}
	key := p.ScopeType + "|" + p.ScopeID
	q, ok := f.quotas[key]
	if !ok {
		q = &store.Quota{ID: f.next("quota"), ScopeType: p.ScopeType, ScopeID: p.ScopeID, CreatedAt: f.Now()}
		f.quotas[key] = q
	}
	q.MaxVCPU, q.MaxRAMMB, q.MaxDiskGB, q.MaxCount = p.MaxVCPU, p.MaxRAMMB, p.MaxDiskGB, p.MaxCount
	q.UpdatedAt = f.Now()
	c := *q
	return &c, nil
}

// computeUsageLocked mirrors PgStore.ComputeUsage in memory. Caller holds f.mu.
func (f *Fake) computeUsageLocked(tenantID string, snapshot map[int]store.Alloc) (store.QuotaUsage, map[string]store.QuotaUsage) {
	byProject := map[string]store.QuotaUsage{}
	var tenant store.QuotaUsage
	for vmid, o := range f.ownership {
		if o.TenantID != tenantID {
			continue
		}
		var a store.Alloc
		switch o.Status {
		case "active":
			al, ok := snapshot[vmid]
			if !ok {
				continue
			}
			a = al
		case "pending":
			if o.ReservedVCPU != nil {
				a.VCPU = *o.ReservedVCPU
			}
			if o.ReservedRAMMB != nil {
				a.RAMMB = *o.ReservedRAMMB
			}
			if o.ReservedDiskGB != nil {
				a.DiskGB = *o.ReservedDiskGB
			}
		default:
			continue
		}
		pu := byProject[o.ProjectID]
		pu.VCPU += a.VCPU
		pu.RAMMB += a.RAMMB
		pu.DiskGB += a.DiskGB
		pu.Count++
		byProject[o.ProjectID] = pu
		tenant.VCPU += a.VCPU
		tenant.RAMMB += a.RAMMB
		tenant.DiskGB += a.DiskGB
		tenant.Count++
	}
	return tenant, byProject
}

func (f *Fake) ComputeUsage(_ context.Context, tenantID string, snapshot map[int]store.Alloc) (store.QuotaUsage, map[string]store.QuotaUsage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ComputeUsage"); err != nil {
		return store.QuotaUsage{}, nil, err
	}
	tenant, byProject := f.computeUsageLocked(tenantID, snapshot)
	return tenant, byProject, nil
}

// checkQuotaFake mirrors store.checkQuota (unexported there) for the fake's
// in-memory reservation path.
func checkQuotaFake(scope string, q *store.Quota, usage store.QuotaUsage, delta store.Alloc) error {
	if q == nil {
		return nil
	}
	if q.MaxVCPU != nil && usage.VCPU+delta.VCPU > *q.MaxVCPU {
		return store.ErrQuotaExceeded{Scope: scope, Dimension: "vcpu", Limit: int64(*q.MaxVCPU), Used: int64(usage.VCPU), Requested: int64(delta.VCPU)}
	}
	if q.MaxRAMMB != nil && usage.RAMMB+delta.RAMMB > *q.MaxRAMMB {
		return store.ErrQuotaExceeded{Scope: scope, Dimension: "ram_mb", Limit: *q.MaxRAMMB, Used: usage.RAMMB, Requested: delta.RAMMB}
	}
	if q.MaxDiskGB != nil && usage.DiskGB+delta.DiskGB > *q.MaxDiskGB {
		return store.ErrQuotaExceeded{Scope: scope, Dimension: "disk_gb", Limit: *q.MaxDiskGB, Used: usage.DiskGB, Requested: delta.DiskGB}
	}
	if q.MaxCount != nil && usage.Count+1 > *q.MaxCount {
		return store.ErrQuotaExceeded{Scope: scope, Dimension: "count", Limit: int64(*q.MaxCount), Used: int64(usage.Count), Requested: 1}
	}
	return nil
}

// ReserveOwnership mirrors the real store's reservation semantics in memory (the
// AdvisoryLock no-op means the true race guard is proven against Postgres, not
// here). It computes usage in-memory, enforces the same project-then-tenant
// checks, and inserts the pending row (duplicate VMID → ErrConflict).
func (f *Fake) ReserveOwnership(_ context.Context, p store.ReserveOwnershipParams) (*store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ReserveOwnership"); err != nil {
		return nil, err
	}
	tenantUsage, byProject := f.computeUsageLocked(p.TenantID, p.Snapshot)
	if err := checkQuotaFake("project", f.quotas["project|"+p.ProjectID], byProject[p.ProjectID], p.Reserved); err != nil {
		return nil, err
	}
	if err := checkQuotaFake("tenant", f.quotas["tenant|"+p.TenantID], tenantUsage, p.Reserved); err != nil {
		return nil, err
	}
	rv, rr, rd := p.Reserved.VCPU, p.Reserved.RAMMB, p.Reserved.DiskGB
	if existing, ok := f.ownership[p.VMID]; ok {
		// A live (active/pending) row is a real conflict; a tombstoned VMID is
		// free — revive it in place (mirrors postgres CreateOwnership ON CONFLICT).
		if existing.Status != "tombstoned" {
			return nil, fmt.Errorf("reserve ownership for vmid %d: %w", p.VMID, store.ErrConflict)
		}
		existing.TenantID, existing.ProjectID = p.TenantID, p.ProjectID
		existing.GuestType, existing.Node, existing.CreatedBy = p.GuestType, p.Node, p.CreatedBy
		existing.Status, existing.PVEUPID = "pending", nil
		existing.ReservedVCPU, existing.ReservedRAMMB, existing.ReservedDiskGB = &rv, &rr, &rd
		existing.UpdatedAt = f.Now()
		c := *existing
		return &c, nil
	}
	id := f.next("own")
	o := &store.ResourceOwnership{
		ID: id, TenantID: p.TenantID, ProjectID: p.ProjectID, VMID: p.VMID, GuestType: p.GuestType,
		Node: p.Node, CreatedBy: p.CreatedBy, Status: "pending",
		ReservedVCPU: &rv, ReservedRAMMB: &rr, ReservedDiskGB: &rd,
		CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	f.ownership[p.VMID] = o
	c := *o
	return &c, nil
}

func (f *Fake) InsertAuditIntent(_ context.Context, a store.AuditIntent) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("InsertAuditIntent"); err != nil {
		return "", err
	}
	id := f.next("audit")
	f.audit = append(f.audit, &store.AuditEntry{
		ID: id, TS: f.Now(), ActorUserID: a.ActorUserID, ActorSystem: a.ActorSystem,
		TenantID: a.TenantID, ProjectID: a.ProjectID,
		Action: a.Action, TargetType: a.TargetType, TargetID: a.TargetID, Outcome: "pending", IP: a.IP,
	})
	return id, nil
}

func (f *Fake) FinalizeAudit(_ context.Context, id, outcome string, detail []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("FinalizeAudit"); err != nil {
		return err
	}
	for _, e := range f.audit {
		if e.ID == id {
			e.Outcome = outcome
			e.Detail = detail
			return nil
		}
	}
	return store.ErrNotFound
}

// AllAudit returns a copy of every audit row (test convenience). Unlike ListAudit
// it ignores the tenant filter, so rows with a nil tenant_id — e.g. tenant.create
// intents written before the tenant exists — are visible for assertions.
func (f *Fake) AllAudit() []store.AuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.AuditEntry, 0, len(f.audit))
	for _, e := range f.audit {
		out = append(out, *e)
	}
	return out
}

// AddAuditEntry seeds a finalized audit row directly (test convenience; bypasses
// the intent/finalize flow). ts orders the row on the spine.
func (f *Fake) AddAuditEntry(e store.AuditEntry) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if e.ID == "" {
		e.ID = f.next("audit")
	}
	c := e
	f.audit = append(f.audit, &c)
	return e.ID
}

func (f *Fake) ListAudit(_ context.Context, aq store.AuditQuery) ([]store.AuditEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ListAudit"); err != nil {
		return nil, err
	}
	// Copy matching rows, then sort ts DESC, id DESC, then apply limit — mirroring
	// the SQL keyset order.
	var matched []store.AuditEntry
	for _, e := range f.audit {
		if e.TenantID == nil || *e.TenantID != aq.TenantID {
			continue
		}
		if aq.Before != nil && !e.TS.Before(*aq.Before) {
			continue
		}
		if aq.ProjectID != "" && (e.ProjectID == nil || *e.ProjectID != aq.ProjectID) {
			continue
		}
		if aq.Outcome != "" && e.Outcome != aq.Outcome {
			continue
		}
		matched = append(matched, *e)
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].TS.Equal(matched[j].TS) {
			return matched[i].TS.After(matched[j].TS)
		}
		return matched[i].ID > matched[j].ID
	})
	limit := aq.Limit
	if limit <= 0 {
		limit = 50
	}
	if len(matched) > limit {
		matched = matched[:limit]
	}
	out := []store.AuditEntry{}
	out = append(out, matched...)
	return out, nil
}

func rank(role string) int {
	switch role {
	case "owner":
		return 3
	case "contributor":
		return 2
	case "reader":
		return 1
	default:
		return 0
	}
}

// --- invitations (Phase 5) ---

func (f *Fake) CreateInvitation(_ context.Context, p store.CreateInvitationParams) (*store.Invitation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("CreateInvitation"); err != nil {
		return nil, err
	}
	// Supersede any still-pending invite for the same (email, scope).
	for id, inv := range f.invitations {
		if inv.AcceptedAt == nil && strings.EqualFold(inv.Email, p.Email) &&
			inv.ScopeType == p.ScopeType && inv.ScopeID == p.ScopeID {
			delete(f.invitations, id)
		}
	}
	// Mirror the UNIQUE(token_hash) constraint.
	for _, inv := range f.invitations {
		if inv.TokenHash == p.TokenHash {
			return nil, fmt.Errorf("create invitation: %w", store.ErrConflict)
		}
	}
	id := f.next("inv")
	inv := &store.Invitation{
		ID: id, TokenHash: p.TokenHash, Email: p.Email, ScopeType: p.ScopeType,
		ScopeID: p.ScopeID, Role: p.Role, InvitedBy: p.InvitedBy, ExpiresAt: p.ExpiresAt,
		CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	f.invitations[id] = inv
	c := *inv
	return &c, nil
}

func (f *Fake) GetInvitationByTokenHash(_ context.Context, tokenHash string) (*store.Invitation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, inv := range f.invitations {
		if inv.TokenHash == tokenHash {
			c := *inv
			return &c, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *Fake) GetInvitationByID(_ context.Context, id string) (*store.Invitation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("GetInvitationByID"); err != nil {
		return nil, err
	}
	if inv, ok := f.invitations[id]; ok {
		c := *inv
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) ListPendingInvitationsByScopes(_ context.Context, scopeType string, scopeIDs []string) ([]store.Invitation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ListPendingInvitationsByScopes"); err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, id := range scopeIDs {
		want[id] = true
	}
	out := []store.Invitation{}
	for _, inv := range f.invitations {
		if inv.ScopeType == scopeType && want[inv.ScopeID] && inv.AcceptedAt == nil {
			out = append(out, *inv)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (f *Fake) MarkInvitationAccepted(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("MarkInvitationAccepted"); err != nil {
		return false, err
	}
	inv, ok := f.invitations[id]
	if !ok || inv.AcceptedAt != nil {
		return false, nil // gone or already accepted (raced)
	}
	t := f.Now()
	inv.AcceptedAt = &t
	inv.UpdatedAt = t
	return true, nil
}

func (f *Fake) DeleteInvitation(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.invitations[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.invitations, id)
	return nil
}

// --- TOTP secrets (Phase 5) ---

func (f *Fake) UpsertTOTPSecret(_ context.Context, userID string, secretEncrypted []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("UpsertTOTPSecret"); err != nil {
		return err
	}
	// ON CONFLICT resets confirmed_at to NULL: re-enroll always starts unconfirmed.
	f.totp[userID] = &store.TOTPSecret{
		UserID:          userID,
		SecretEncrypted: append([]byte(nil), secretEncrypted...),
		ConfirmedAt:     nil,
	}
	return nil
}

func (f *Fake) GetTOTPSecret(_ context.Context, userID string) (*store.TOTPSecret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.totp[userID]; ok {
		c := *t
		c.SecretEncrypted = append([]byte(nil), t.SecretEncrypted...)
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) ConfirmTOTPSecret(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.totp[userID]
	if !ok || t.ConfirmedAt != nil {
		return store.ErrNotFound // nothing unconfirmed to confirm
	}
	now := f.Now()
	t.ConfirmedAt = &now
	return nil
}

func (f *Fake) DeleteTOTPSecret(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.totp, userID) // idempotent
	return nil
}

// --- recovery codes (Phase 5) ---

func (f *Fake) ReplaceRecoveryCodes(_ context.Context, userID string, codeHashes []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ReplaceRecoveryCodes"); err != nil {
		return err
	}
	// Delete all of the user's codes, then insert the new set.
	kept := f.recovery[:0:0]
	for _, rc := range f.recovery {
		if rc.UserID != userID {
			kept = append(kept, rc)
		}
	}
	for _, h := range codeHashes {
		kept = append(kept, &fakeRecoveryCode{UserID: userID, CodeHash: h})
	}
	f.recovery = kept
	return nil
}

func (f *Fake) ConsumeRecoveryCode(_ context.Context, userID, codeHash string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ConsumeRecoveryCode"); err != nil {
		return false, err
	}
	for _, rc := range f.recovery {
		if rc.UserID == userID && rc.CodeHash == codeHash && rc.UsedAt == nil {
			t := f.Now()
			rc.UsedAt = &t
			return true, nil
		}
	}
	return false, nil // unknown or already-used code
}

func (f *Fake) CountUnusedRecoveryCodes(_ context.Context, userID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, rc := range f.recovery {
		if rc.UserID == userID && rc.UsedAt == nil {
			n++
		}
	}
	return n, nil
}

func (f *Fake) DeleteRecoveryCodes(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.recovery[:0:0]
	for _, rc := range f.recovery {
		if rc.UserID != userID {
			kept = append(kept, rc)
		}
	}
	f.recovery = kept
	return nil
}

// --- login challenges (Phase 5) ---

func (f *Fake) CreateLoginChallenge(_ context.Context, p store.CreateLoginChallengeParams) (*store.LoginChallenge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("CreateLoginChallenge"); err != nil {
		return nil, err
	}
	if _, ok := f.chalByHash[p.TokenHash]; ok {
		return nil, fmt.Errorf("create login challenge: %w", store.ErrConflict)
	}
	id := f.next("chal")
	lc := store.LoginChallenge{
		ID: id, UserID: p.UserID, Attempts: 0, CreatedAt: f.Now(),
		ExpiresAt: p.ExpiresAt, IP: p.IP, UserAgent: p.UserAgent,
	}
	f.challenges[id] = &fakeChallenge{c: lc, tokenHash: p.TokenHash}
	f.chalByHash[p.TokenHash] = id
	c := lc
	return &c, nil
}

func (f *Fake) GetLoginChallengeByTokenHash(_ context.Context, tokenHash string) (*store.LoginChallenge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.chalByHash[tokenHash]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := f.challenges[id].c
	return &c, nil
}

func (f *Fake) ConsumeLoginChallenge(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ConsumeLoginChallenge"); err != nil {
		return false, err
	}
	ch, ok := f.challenges[id]
	if !ok || ch.c.ConsumedAt != nil {
		return false, nil // gone or already consumed
	}
	t := f.Now()
	ch.c.ConsumedAt = &t
	return true, nil
}

func (f *Fake) RecordChallengeFailure(_ context.Context, id string, maxAttempts int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("RecordChallengeFailure"); err != nil {
		return false, err
	}
	ch, ok := f.challenges[id]
	if !ok || ch.c.ConsumedAt != nil {
		return true, nil // already consumed/locked (or gone)
	}
	ch.c.Attempts++
	if ch.c.Attempts >= maxAttempts {
		t := f.Now()
		ch.c.ConsumedAt = &t
		return true, nil
	}
	return false, nil
}

// --- jobs (scheduler, ADR-0018) ---

// AllJobs returns a copy of every job row (test convenience), unordered.
func (f *Fake) AllJobs() []store.Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Job, 0, len(f.jobs))
	for _, j := range f.jobs {
		out = append(out, *j)
	}
	return out
}

// JobStatus returns the current status of the job with id, or "" if none — a
// convenience for claim/complete/fail assertions.
func (f *Fake) JobStatus(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j, ok := f.jobs[id]; ok {
		return j.Status
	}
	return ""
}

func (f *Fake) EnqueueJob(_ context.Context, p store.EnqueueJobParams) (*store.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("EnqueueJob"); err != nil {
		return nil, err
	}
	maxAttempts := p.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	missed := p.MissedPolicy
	if missed == "" {
		missed = "catch_up"
	}
	id := f.next("job")
	j := &store.Job{
		ID: id, Kind: p.Kind, Handler: p.Handler, TenantID: p.TenantID, ProjectID: p.ProjectID,
		VMID: p.VMID, Payload: p.Payload, Cron: p.Cron, Timezone: p.Timezone, RunAt: p.RunAt,
		Status: "scheduled", Attempts: 0, MaxAttempts: maxAttempts, MissedPolicy: missed,
		CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	f.jobs[id] = j
	c := *j
	return &c, nil
}

func (f *Fake) ClaimDueJobs(_ context.Context, now time.Time, limit int, lockedBy string) ([]store.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ClaimDueJobs"); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 10
	}
	var due []*store.Job
	for _, j := range f.jobs {
		if j.Status == "scheduled" && !j.RunAt.After(now) {
			due = append(due, j)
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].RunAt.Before(due[j].RunAt) })
	out := []store.Job{}
	for _, j := range due {
		if len(out) >= limit {
			break
		}
		t := now
		lb := lockedBy
		j.Status = "running"
		j.LockedAt = &t
		j.LockedBy = &lb
		j.UpdatedAt = f.Now()
		out = append(out, *j)
	}
	return out, nil
}

func (f *Fake) ReclaimStaleRunning(_ context.Context, olderThan time.Time) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, j := range f.jobs {
		if j.Status == "running" && j.LockedAt != nil && j.LockedAt.Before(olderThan) {
			j.Status = "scheduled"
			j.LockedAt = nil
			j.LockedBy = nil
			j.UpdatedAt = f.Now()
			n++
		}
	}
	return n, nil
}

// CompleteJob no-ops unless the job is still 'running' (mirrors the Pg
// `status='running'` guard): a job cancelled mid-run is never resurrected.
func (f *Fake) CompleteJob(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok || j.Status != "running" {
		return nil // missing or raced to cancelled/terminal — no-op
	}
	j.Status = "succeeded"
	j.LockedAt = nil
	j.LockedBy = nil
	j.UpdatedAt = f.Now()
	return nil
}

func (f *Fake) RescheduleRecurring(_ context.Context, id string, nextRunAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[id]
	if !ok || j.Status != "running" {
		return nil // guarded: never re-arm a cancelled/terminal job
	}
	j.Status = "scheduled"
	j.RunAt = nextRunAt
	j.Attempts = 0
	j.LastError = nil
	j.LockedAt = nil
	j.LockedBy = nil
	j.UpdatedAt = f.Now()
	return nil
}

func (f *Fake) FailJob(_ context.Context, id, lastErr string, retryAt time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("FailJob"); err != nil {
		return false, err
	}
	j, ok := f.jobs[id]
	if !ok || j.Status != "running" {
		return false, nil // guarded: raced to cancelled/terminal — no-op
	}
	j.Attempts++
	le := lastErr
	j.LastError = &le
	j.LockedAt = nil
	j.LockedBy = nil
	j.UpdatedAt = f.Now()
	if j.Attempts >= j.MaxAttempts {
		j.Status = "failed"
		return true, nil
	}
	j.Status = "scheduled"
	j.RunAt = retryAt
	return false, nil
}

func (f *Fake) BumpScheduledRunAt(_ context.Context, id string, runAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("BumpScheduledRunAt"); err != nil {
		return err
	}
	j, ok := f.jobs[id]
	if !ok || j.Status != "scheduled" {
		return store.ErrNotFound
	}
	j.RunAt = runAt
	j.UpdatedAt = f.Now()
	return nil
}

func (f *Fake) CancelJobsForVMID(_ context.Context, vmid int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("CancelJobsForVMID"); err != nil {
		return 0, err
	}
	n := 0
	for _, j := range f.jobs {
		if j.VMID != nil && *j.VMID == vmid && (j.Status == "scheduled" || j.Status == "running") {
			j.Status = "cancelled"
			j.LockedAt = nil
			j.LockedBy = nil
			j.UpdatedAt = f.Now()
			n++
		}
	}
	return n, nil
}

func (f *Fake) CancelJobsForVMIDByPrefix(_ context.Context, vmid int, prefix string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("CancelJobsForVMIDByPrefix"); err != nil {
		return 0, err
	}
	n := 0
	for _, j := range f.jobs {
		if j.VMID != nil && *j.VMID == vmid && strings.HasPrefix(j.Handler, prefix) &&
			(j.Status == "scheduled" || j.Status == "running") {
			j.Status = "cancelled"
			j.LockedAt = nil
			j.LockedBy = nil
			j.UpdatedAt = f.Now()
			n++
		}
	}
	return n, nil
}

func (f *Fake) GetJob(_ context.Context, id string) (*store.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if j, ok := f.jobs[id]; ok {
		c := *j
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) ListJobs(_ context.Context, fl store.JobFilter) ([]store.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ListJobs"); err != nil {
		return nil, err
	}
	var matched []store.Job
	for _, j := range f.jobs {
		if fl.TenantID != "" && (j.TenantID == nil || *j.TenantID != fl.TenantID) {
			continue
		}
		if fl.Status != "" && j.Status != fl.Status {
			continue
		}
		if fl.VMID != nil && (j.VMID == nil || *j.VMID != *fl.VMID) {
			continue
		}
		matched = append(matched, *j)
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].RunAt.Equal(matched[j].RunAt) {
			return matched[i].RunAt.After(matched[j].RunAt)
		}
		return matched[i].ID > matched[j].ID
	})
	limit := fl.Limit
	if limit <= 0 {
		limit = 100
	}
	if len(matched) > limit {
		matched = matched[:limit]
	}
	out := []store.Job{}
	out = append(out, matched...)
	return out, nil
}

// --- schedules (auto-shutdown, ADR-0019) ---

// AllSchedules returns a copy of every schedule row (test convenience), unordered.
func (f *Fake) AllSchedules() []store.Schedule {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.Schedule, 0, len(f.schedules))
	for _, s := range f.schedules {
		out = append(out, *s)
	}
	return out
}

// findResourceScheduleLocked returns the resource-scope schedule for vmid (caller
// holds f.mu).
func (f *Fake) findResourceScheduleLocked(vmid int) *store.Schedule {
	for _, s := range f.schedules {
		if s.Scope == "resource" && s.VMID != nil && *s.VMID == vmid {
			return s
		}
	}
	return nil
}

// findProjectScheduleLocked returns the project-scope schedule for (tenant,
// project) (caller holds f.mu).
func (f *Fake) findProjectScheduleLocked(tenantID, projectID string) *store.Schedule {
	for _, s := range f.schedules {
		if s.Scope == "project" && s.TenantID == tenantID && s.ProjectID == projectID {
			return s
		}
	}
	return nil
}

func (f *Fake) UpsertResourceSchedule(_ context.Context, p store.UpsertResourceScheduleParams) (*store.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("UpsertResourceSchedule"); err != nil {
		return nil, err
	}
	vmid := p.VMID
	s := f.findResourceScheduleLocked(vmid)
	if s == nil {
		s = &store.Schedule{ID: f.next("sched"), Scope: "resource", VMID: &vmid, CreatedAt: f.Now()}
		f.schedules[s.ID] = s
	}
	s.TenantID, s.ProjectID = p.TenantID, p.ProjectID
	s.ShutdownTime, s.AutoStartTime = p.ShutdownTime, p.AutoStartTime
	s.DaysOfWeek = append([]int(nil), p.DaysOfWeek...)
	s.Timezone, s.GraceSeconds = p.Timezone, p.GraceSeconds
	s.Enabled, s.OptOut, s.CreatedBy = p.Enabled, p.OptOut, p.CreatedBy
	s.UpdatedAt = f.Now()
	c := *s
	return &c, nil
}

func (f *Fake) UpsertProjectSchedule(_ context.Context, p store.UpsertProjectScheduleParams) (*store.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("UpsertProjectSchedule"); err != nil {
		return nil, err
	}
	s := f.findProjectScheduleLocked(p.TenantID, p.ProjectID)
	if s == nil {
		s = &store.Schedule{ID: f.next("sched"), Scope: "project", CreatedAt: f.Now()}
		f.schedules[s.ID] = s
	}
	s.TenantID, s.ProjectID = p.TenantID, p.ProjectID
	s.VMID = nil
	s.ShutdownTime, s.AutoStartTime = p.ShutdownTime, p.AutoStartTime
	s.DaysOfWeek = append([]int(nil), p.DaysOfWeek...)
	s.Timezone, s.GraceSeconds = p.Timezone, p.GraceSeconds
	s.Enabled, s.OptOut, s.CreatedBy = p.Enabled, false, p.CreatedBy
	s.UpdatedAt = f.Now()
	c := *s
	return &c, nil
}

func (f *Fake) GetResourceSchedule(_ context.Context, vmid int) (*store.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("GetResourceSchedule"); err != nil {
		return nil, err
	}
	if s := f.findResourceScheduleLocked(vmid); s != nil {
		c := *s
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) GetProjectSchedule(_ context.Context, tenantID, projectID string) (*store.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("GetProjectSchedule"); err != nil {
		return nil, err
	}
	if s := f.findProjectScheduleLocked(tenantID, projectID); s != nil {
		c := *s
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) ListSchedulesByProject(_ context.Context, projectID string) ([]store.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ListSchedulesByProject"); err != nil {
		return nil, err
	}
	out := []store.Schedule{}
	for _, s := range f.schedules {
		if s.ProjectID == projectID {
			out = append(out, *s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Scope != out[j].Scope {
			return out[i].Scope < out[j].Scope
		}
		vi, vj := 0, 0
		if out[i].VMID != nil {
			vi = *out[i].VMID
		}
		if out[j].VMID != nil {
			vj = *out[j].VMID
		}
		return vi < vj
	})
	return out, nil
}

func (f *Fake) DeleteResourceSchedule(_ context.Context, vmid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("DeleteResourceSchedule"); err != nil {
		return err
	}
	if s := f.findResourceScheduleLocked(vmid); s != nil {
		delete(f.schedules, s.ID)
		return nil
	}
	return store.ErrNotFound
}

func (f *Fake) DeleteProjectSchedule(_ context.Context, tenantID, projectID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("DeleteProjectSchedule"); err != nil {
		return err
	}
	if s := f.findProjectScheduleLocked(tenantID, projectID); s != nil {
		delete(f.schedules, s.ID)
		return nil
	}
	return store.ErrNotFound
}

// --- TTL / ephemeral resources (ADR-0020) ---

func ttlPolicyKey(tenantID, projectID string) string { return tenantID + "|" + projectID }

// TTLByVMID returns a copy of a guest's TTL, or nil (test convenience).
func (f *Fake) TTLByVMID(vmid int) *store.TTL {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.ttls[vmid]; ok {
		c := *t
		return &c
	}
	return nil
}

func (f *Fake) UpsertTTL(_ context.Context, p store.UpsertTTLParams) (*store.TTL, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("UpsertTTL"); err != nil {
		return nil, err
	}
	existing, ok := f.ttls[p.VMID]
	id := f.next("ttl")
	created := f.Now()
	if ok {
		id = existing.ID
		created = existing.CreatedAt
	}
	t := &store.TTL{
		ID: id, TenantID: p.TenantID, ProjectID: p.ProjectID, VMID: p.VMID,
		ExpiresAt: p.ExpiresAt, Action: p.Action, Warned24h: false, Warned1h: false,
		OriginalDuration: p.OriginalDuration, CreatedBy: p.CreatedBy,
		CreatedAt: created, UpdatedAt: f.Now(),
	}
	f.ttls[p.VMID] = t
	c := *t
	return &c, nil
}

func (f *Fake) GetTTL(_ context.Context, vmid int) (*store.TTL, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.ttls[vmid]; ok {
		c := *t
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) DeleteTTL(_ context.Context, vmid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.ttls[vmid]; !ok {
		return store.ErrNotFound
	}
	delete(f.ttls, vmid)
	return nil
}

func (f *Fake) SetTTLWarned(_ context.Context, vmid int, which string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.ttls[vmid]
	if !ok {
		return store.ErrNotFound
	}
	if which == "1h" {
		t.Warned1h = true
	} else {
		t.Warned24h = true
	}
	t.UpdatedAt = f.Now()
	return nil
}

func (f *Fake) UpdateTTLExpiry(_ context.Context, vmid int, expiresAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.ttls[vmid]
	if !ok {
		return store.ErrNotFound
	}
	t.ExpiresAt = expiresAt
	t.Warned24h = false
	t.Warned1h = false
	t.UpdatedAt = f.Now()
	return nil
}

func (f *Fake) ListTTLsByProject(_ context.Context, projectID string) ([]store.TTL, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.TTL{}
	for _, t := range f.ttls {
		if t.ProjectID == projectID {
			out = append(out, *t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ExpiresAt.Before(out[j].ExpiresAt) })
	return out, nil
}

func (f *Fake) GetProjectTTLPolicy(_ context.Context, tenantID, projectID string) (*store.ProjectTTLPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.ttlPolicies[ttlPolicyKey(tenantID, projectID)]; ok {
		c := *p
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) UpsertProjectTTLPolicy(_ context.Context, p store.UpsertProjectTTLPolicyParams) (*store.ProjectTTLPolicy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("UpsertProjectTTLPolicy"); err != nil {
		return nil, err
	}
	key := ttlPolicyKey(p.TenantID, p.ProjectID)
	created := f.Now()
	if existing, ok := f.ttlPolicies[key]; ok {
		created = existing.CreatedAt
	}
	pol := &store.ProjectTTLPolicy{
		TenantID: p.TenantID, ProjectID: p.ProjectID,
		DefaultTTL: p.DefaultTTL, MaxTTL: p.MaxTTL,
		CreatedAt: created, UpdatedAt: f.Now(),
	}
	f.ttlPolicies[key] = pol
	c := *pol
	return &c, nil
}

// --- deployment sets (ADR-0029) ---

// AddDeploymentSet seeds a set row directly and returns its id (test convenience).
func (f *Fake) AddDeploymentSet(tenantID, projectID, serviceID, status string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next("set")
	if status == "" {
		status = "provisioning"
	}
	f.deploymentSets[id] = &store.DeploymentSet{
		ID: id, TenantID: tenantID, ProjectID: projectID, ServiceID: serviceID,
		Status: status, CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	return id
}

// SetStatus returns the current status of a deployment set, or "" (test convenience).
func (f *Fake) SetStatus(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if d, ok := f.deploymentSets[id]; ok {
		return d.Status
	}
	return ""
}

func (f *Fake) CreateDeploymentSet(_ context.Context, p store.CreateDeploymentSetParams) (*store.DeploymentSet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("CreateDeploymentSet"); err != nil {
		return nil, err
	}
	status := p.Status
	if status == "" {
		status = "provisioning"
	}
	id := f.next("set")
	d := &store.DeploymentSet{
		ID: id, TenantID: p.TenantID, ProjectID: p.ProjectID, ServiceID: p.ServiceID,
		Status: status, CreatedAt: f.Now(), UpdatedAt: f.Now(),
	}
	f.deploymentSets[id] = d
	c := *d
	return &c, nil
}

func (f *Fake) GetDeploymentSet(_ context.Context, id string) (*store.DeploymentSet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("GetDeploymentSet"); err != nil {
		return nil, err
	}
	if d, ok := f.deploymentSets[id]; ok {
		c := *d
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) ListDeploymentSets(_ context.Context, tenantID string) ([]store.DeploymentSet, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ListDeploymentSets"); err != nil {
		return nil, err
	}
	out := []store.DeploymentSet{}
	for _, d := range f.deploymentSets {
		if d.TenantID == tenantID {
			out = append(out, *d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (f *Fake) ListSetMembers(_ context.Context, setID string) ([]store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ListSetMembers"); err != nil {
		return nil, err
	}
	out := []store.ResourceOwnership{}
	for _, o := range f.ownership {
		if o.DeploymentSetID != nil && *o.DeploymentSetID == setID {
			out = append(out, *o)
		}
	}
	// role DESC then vmid: 'server' before 'agent' (start order); callers reverse.
	sort.Slice(out, func(i, j int) bool {
		ri, rj := "", ""
		if out[i].Role != nil {
			ri = *out[i].Role
		}
		if out[j].Role != nil {
			rj = *out[j].Role
		}
		if ri != rj {
			return ri > rj
		}
		return out[i].VMID < out[j].VMID
	})
	return out, nil
}

func (f *Fake) UpdateSetStatus(_ context.Context, id, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("UpdateSetStatus"); err != nil {
		return err
	}
	d, ok := f.deploymentSets[id]
	if !ok {
		return store.ErrNotFound
	}
	d.Status = status
	d.UpdatedAt = f.Now()
	return nil
}

func (f *Fake) DeleteDeploymentSet(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("DeleteDeploymentSet"); err != nil {
		return err
	}
	if _, ok := f.deploymentSets[id]; !ok {
		return store.ErrNotFound
	}
	// Mirror ON DELETE SET NULL: null out members' set linkage.
	for _, o := range f.ownership {
		if o.DeploymentSetID != nil && *o.DeploymentSetID == id {
			o.DeploymentSetID = nil
			o.Role = nil
		}
	}
	delete(f.deploymentSets, id)
	return nil
}

// ReserveOwnershipBatch mirrors the real store's atomic multi-guest reservation:
// it computes usage once, accumulates each accepted member's delta into the
// running usage before the next member's quota check (the count-accumulation fix),
// and inserts N pending rows tagged with the set id + role — all-or-nothing.
func (f *Fake) ReserveOwnershipBatch(_ context.Context, p store.ReserveOwnershipBatchParams) ([]store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("ReserveOwnershipBatch"); err != nil {
		return nil, err
	}
	tenantUsage, byProject := f.computeUsageLocked(p.TenantID, p.Snapshot)
	projUsage := byProject[p.ProjectID]
	tenUsage := tenantUsage
	projQuota := f.quotas["project|"+p.ProjectID]
	tenQuota := f.quotas["tenant|"+p.TenantID]
	for _, m := range p.Members {
		if err := checkQuotaFake("project", projQuota, projUsage, m.Reserved); err != nil {
			return nil, err
		}
		if err := checkQuotaFake("tenant", tenQuota, tenUsage, m.Reserved); err != nil {
			return nil, err
		}
		accumulate(&projUsage, m.Reserved)
		accumulate(&tenUsage, m.Reserved)
	}
	// Reserve the VMIDs, respecting the tombstone-revive rule. If any conflicts,
	// roll back the ones already inserted (all-or-nothing).
	setID := p.SetID
	inserted := []int{}
	out := []store.ResourceOwnership{}
	rollback := func() {
		for _, vmid := range inserted {
			delete(f.ownership, vmid)
		}
	}
	for _, m := range p.Members {
		rv, rr, rd := m.Reserved.VCPU, m.Reserved.RAMMB, m.Reserved.DiskGB
		role := m.Role
		if existing, ok := f.ownership[m.VMID]; ok {
			if existing.Status != "tombstoned" {
				rollback()
				return nil, fmt.Errorf("reserve ownership for vmid %d: %w", m.VMID, store.ErrConflict)
			}
			existing.TenantID, existing.ProjectID = p.TenantID, p.ProjectID
			existing.GuestType, existing.Node, existing.CreatedBy = m.GuestType, m.Node, p.CreatedBy
			existing.Status, existing.PVEUPID = "pending", nil
			existing.ReservedVCPU, existing.ReservedRAMMB, existing.ReservedDiskGB = &rv, &rr, &rd
			existing.DeploymentSetID, existing.Role = &setID, &role
			existing.UpdatedAt = f.Now()
			out = append(out, *existing)
			continue
		}
		id := f.next("own")
		o := &store.ResourceOwnership{
			ID: id, TenantID: p.TenantID, ProjectID: p.ProjectID, VMID: m.VMID, GuestType: m.GuestType,
			Node: m.Node, CreatedBy: p.CreatedBy, Status: "pending",
			ReservedVCPU: &rv, ReservedRAMMB: &rr, ReservedDiskGB: &rd,
			DeploymentSetID: &setID, Role: &role, CreatedAt: f.Now(), UpdatedAt: f.Now(),
		}
		f.ownership[m.VMID] = o
		inserted = append(inserted, m.VMID)
		out = append(out, *o)
	}
	return out, nil
}

// accumulate folds one member's reserved allocation into a running usage total
// (count += 1), mirroring store.addAlloc for the fake's batch path.
func accumulate(u *store.QuotaUsage, a store.Alloc) {
	u.VCPU += a.VCPU
	u.RAMMB += a.RAMMB
	u.DiskGB += a.DiskGB
	u.Count++
}

// --- set schedules (ADR-0029) ---

func (f *Fake) findSetScheduleLocked(tenantID, setID string) *store.Schedule {
	for _, s := range f.schedules {
		if s.Scope == "set" && s.TenantID == tenantID && s.SetID != nil && *s.SetID == setID {
			return s
		}
	}
	return nil
}

func (f *Fake) UpsertSetSchedule(_ context.Context, p store.UpsertSetScheduleParams) (*store.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("UpsertSetSchedule"); err != nil {
		return nil, err
	}
	setID := p.SetID
	s := f.findSetScheduleLocked(p.TenantID, p.SetID)
	if s == nil {
		s = &store.Schedule{ID: f.next("sched"), Scope: "set", SetID: &setID, CreatedAt: f.Now()}
		f.schedules[s.ID] = s
	}
	s.TenantID, s.ProjectID = p.TenantID, p.ProjectID
	s.VMID = nil
	s.SetID = &setID
	s.ShutdownTime, s.AutoStartTime = p.ShutdownTime, p.AutoStartTime
	s.DaysOfWeek = append([]int(nil), p.DaysOfWeek...)
	s.Timezone, s.GraceSeconds = p.Timezone, p.GraceSeconds
	s.Enabled, s.OptOut, s.CreatedBy = p.Enabled, false, p.CreatedBy
	s.UpdatedAt = f.Now()
	c := *s
	return &c, nil
}

func (f *Fake) GetSetSchedule(_ context.Context, tenantID, setID string) (*store.Schedule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("GetSetSchedule"); err != nil {
		return nil, err
	}
	if s := f.findSetScheduleLocked(tenantID, setID); s != nil {
		c := *s
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *Fake) DeleteSetSchedule(_ context.Context, tenantID, setID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.failed("DeleteSetSchedule"); err != nil {
		return err
	}
	if s := f.findSetScheduleLocked(tenantID, setID); s != nil {
		delete(f.schedules, s.ID)
		return nil
	}
	return store.ErrNotFound
}
