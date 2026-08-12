package auth

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/timkrebs9/proxcloud/backend/internal/store"
)

// fakeStore is an in-memory store.Store for auth unit tests. It implements the
// full interface but only the users/sessions/memberships/tenant slices used by
// the auth package carry real behavior; the rest are minimal stubs. Its methods
// are mutex-guarded so concurrent use is data-race-free, but its AdvisoryLock
// and WithTx are no-ops — the real bootstrap race guard (advisory lock +
// count-then-insert) is proven against Postgres by store.TestBootstrapRaceGuard.
type fakeStore struct {
	mu          sync.Mutex
	seq         int
	users       map[string]*store.User
	sessions    map[string]*store.Session
	memberships map[string]*store.Membership
	tenants     map[string]*store.Tenant         // by slug
	projects    map[string]*store.Project        // by id
	ownership   map[int]*store.ResourceOwnership // by vmid

	// Phase 5 (ADR-0013) aggregates.
	invitations map[string]*store.Invitation // by id
	totp        map[string]*store.TOTPSecret // by userID
	recovery    []*fakeRecoveryCode          // single-use via UsedAt
	challenges  map[string]*fakeChallenge    // by id
	chalByHash  map[string]string            // token_hash -> challenge id

	// audit records intent/finalize rows so the chunk-C security-mutation tests
	// can assert one row per mutation (mirrors storetest.Fake.AllAudit).
	audit []*store.AuditEntry
	fail  map[string]error // method name -> forced error (fail-closed coverage)

	now func() time.Time
}

// fakeRecoveryCode is one stored recovery code (hashed, single-use).
type fakeRecoveryCode struct {
	UserID   string
	CodeHash string
	UsedAt   *time.Time
}

// fakeChallenge wraps a LoginChallenge with the token hash the domain struct omits.
type fakeChallenge struct {
	c         store.LoginChallenge
	tokenHash string
}

func newFakeStore() *fakeStore {
	f := &fakeStore{
		users:       map[string]*store.User{},
		sessions:    map[string]*store.Session{},
		memberships: map[string]*store.Membership{},
		tenants:     map[string]*store.Tenant{},
		projects:    map[string]*store.Project{},
		ownership:   map[int]*store.ResourceOwnership{},
		invitations: map[string]*store.Invitation{},
		totp:        map[string]*store.TOTPSecret{},
		recovery:    []*fakeRecoveryCode{},
		challenges:  map[string]*fakeChallenge{},
		chalByHash:  map[string]string{},
		audit:       []*store.AuditEntry{},
		fail:        map[string]error{},
		now:         time.Now,
	}
	f.tenants["default"] = &store.Tenant{ID: "tenant-default", Name: "Default", Slug: "default"}
	return f
}

// failOn forces method (by name) to return err until cleared with a nil err.
func (f *fakeStore) failOn(method string, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err == nil {
		delete(f.fail, method)
		return
	}
	f.fail[method] = err
}

// allAudit returns a copy of every recorded audit row (test convenience).
func (f *fakeStore) allAudit() []store.AuditEntry {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.AuditEntry, 0, len(f.audit))
	for _, e := range f.audit {
		out = append(out, *e)
	}
	return out
}

func (f *fakeStore) nextID(prefix string) string {
	f.seq++
	return fmt.Sprintf("%s-%d", prefix, f.seq)
}

var _ store.Store = (*fakeStore)(nil)

// --- base ---

func (f *fakeStore) Ping(context.Context) error                { return nil }
func (f *fakeStore) RunMigrations() (uint, error)              { return 1, nil }
func (f *fakeStore) AdvisoryLock(context.Context, int64) error { return nil }

// WithTx runs fn against the same store; the fake has no isolation but the
// mutex inside each method keeps concurrent bootstrap attempts serialized
// enough to exercise the CountUsers-then-insert guard.
func (f *fakeStore) WithTx(ctx context.Context, fn func(store.Store) error) error {
	return fn(f)
}

func (f *fakeStore) GetTenantBySlug(_ context.Context, slug string) (*store.Tenant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tenants[slug]; ok {
		return t, nil
	}
	return nil, store.ErrNotFound
}
func (f *fakeStore) ListTenants(context.Context) ([]store.Tenant, error) { return nil, nil }
func (f *fakeStore) GetProjectByPoolID(context.Context, string) (*store.Project, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) ListProjectsByTenant(context.Context, string) ([]store.Project, error) {
	return nil, nil
}

// --- users ---

func (f *fakeStore) CreateUser(_ context.Context, p store.CreateUserParams) (*store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["CreateUser"]; err != nil {
		return nil, err
	}
	// Mirror UNIQUE(lower(email)): a duplicate email is a conflict.
	for _, u := range f.users {
		if strings.EqualFold(u.Email, p.Email) {
			return nil, fmt.Errorf("create user: %w", store.ErrConflict)
		}
	}
	now := f.now()
	u := &store.User{
		ID:              f.nextID("user"),
		Email:           p.Email,
		DisplayName:     p.DisplayName,
		PasswordHash:    strPtr(p.PasswordHash),
		PasswordAlgo:    strPtr(p.PasswordAlgo),
		IsPlatformAdmin: p.IsPlatformAdmin,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	f.users[u.ID] = u
	return cloneUser(u), nil
}

func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (*store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if strings.EqualFold(u.Email, email) {
			return cloneUser(u), nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) GetUserByID(_ context.Context, id string) (*store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[id]; ok {
		return cloneUser(u), nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) CountUsers(context.Context) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.users), nil
}

func (f *fakeStore) UpdatePasswordHash(_ context.Context, userID, hash, algo string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[userID]
	if !ok {
		return store.ErrNotFound
	}
	u.PasswordHash = strPtr(hash)
	u.PasswordAlgo = strPtr(algo)
	u.UpdatedAt = f.now()
	return nil
}

func (f *fakeStore) SetTOTPEnabled(_ context.Context, userID string, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[userID]
	if !ok {
		return store.ErrNotFound
	}
	u.TOTPEnabled = enabled
	return nil
}

// --- sessions ---

func (f *fakeStore) CreateSession(_ context.Context, p store.CreateSessionParams) (*store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	s := &store.Session{
		ID:                f.nextID("sess"),
		TokenHash:         p.TokenHash,
		UserID:            p.UserID,
		CreatedAt:         now,
		LastSeenAt:        now,
		AbsoluteExpiresAt: p.AbsoluteExpiresAt,
		IP:                p.IP,
		UserAgent:         p.UserAgent,
	}
	f.sessions[s.ID] = s
	return cloneSession(s), nil
}

func (f *fakeStore) GetSessionByTokenHash(_ context.Context, tokenHash string) (*store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.TokenHash == tokenHash {
			return cloneSession(s), nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) TouchSession(_ context.Context, sessionID string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[sessionID]; ok {
		s.LastSeenAt = at
	}
	return nil
}

func (f *fakeStore) SetSessionActiveTenant(_ context.Context, sessionID string, tenantID *string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[sessionID]
	if !ok {
		return store.ErrNotFound
	}
	s.ActiveTenantID = tenantID
	return nil
}

func (f *fakeStore) RevokeSession(_ context.Context, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[sessionID]; ok && s.RevokedAt == nil {
		t := f.now()
		s.RevokedAt = &t
	}
	return nil
}

func (f *fakeStore) RevokeOtherUserSessions(_ context.Context, userID, keepSessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.now()
	for _, s := range f.sessions {
		if s.UserID == userID && s.ID != keepSessionID && s.RevokedAt == nil {
			rt := t
			s.RevokedAt = &rt
		}
	}
	return nil
}

func (f *fakeStore) ListSessionsByUser(_ context.Context, userID string) ([]store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	out := []store.Session{}
	for _, s := range f.sessions {
		if s.UserID == userID && s.RevokedAt == nil && s.AbsoluteExpiresAt.After(now) {
			out = append(out, *cloneSession(s))
		}
	}
	return out, nil
}

// --- memberships ---

func (f *fakeStore) CreateMembership(_ context.Context, p store.CreateMembershipParams) (*store.Membership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	m := &store.Membership{
		ID:        f.nextID("mem"),
		UserID:    p.UserID,
		ScopeType: p.ScopeType,
		ScopeID:   p.ScopeID,
		Role:      p.Role,
		CreatedAt: now,
		UpdatedAt: now,
	}
	f.memberships[m.ID] = m
	return m, nil
}

func (f *fakeStore) ListMembershipsByUser(_ context.Context, userID string) ([]store.Membership, error) {
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

func (f *fakeStore) ListMembershipsByScope(_ context.Context, scopeType, scopeID string) ([]store.Membership, error) {
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

func (f *fakeStore) ListMembershipsByScopes(_ context.Context, scopeType string, scopeIDs []string) ([]store.Membership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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

func (f *fakeStore) GetEffectiveRoles(_ context.Context, userID, tenantID string) (string, map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tenantRole := ""
	projectRoles := map[string]string{}
	for _, m := range f.memberships {
		if m.UserID != userID {
			continue
		}
		switch m.ScopeType {
		case "tenant":
			if m.ScopeID == tenantID && fakeRoleRank(m.Role) > fakeRoleRank(tenantRole) {
				tenantRole = m.Role
			}
		case "project":
			if p, ok := f.projects[m.ScopeID]; ok && p.TenantID == tenantID &&
				fakeRoleRank(m.Role) > fakeRoleRank(projectRoles[m.ScopeID]) {
				projectRoles[m.ScopeID] = m.Role
			}
		}
	}
	return tenantRole, projectRoles, nil
}

// --- users (Phase 3) ---

func (f *fakeStore) ListUsersByIDs(_ context.Context, ids []string) (map[string]store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]store.User{}
	for _, id := range ids {
		if u, ok := f.users[id]; ok {
			out[id] = *cloneUser(u)
		}
	}
	return out, nil
}

// --- tenants (Phase 3) ---

func (f *fakeStore) CreateTenant(_ context.Context, p store.CreateTenantParams) (*store.Tenant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.tenants[p.Slug]; ok {
		return nil, fmt.Errorf("create tenant: %w", store.ErrConflict)
	}
	now := f.now()
	t := &store.Tenant{ID: f.nextID("tenant"), Name: p.Name, Slug: p.Slug, CreatedAt: now, UpdatedAt: now}
	f.tenants[p.Slug] = t
	return cloneTenant(t), nil
}

func (f *fakeStore) GetTenantByID(_ context.Context, id string) (*store.Tenant, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tenants {
		if t.ID == id {
			return cloneTenant(t), nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) ListTenantsForUser(_ context.Context, userID string) ([]store.TenantWithRole, error) {
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
		if tid != "" && fakeRoleRank(m.Role) > fakeRoleRank(roleByTenant[tid]) {
			roleByTenant[tid] = m.Role
		}
	}
	out := []store.TenantWithRole{}
	for _, t := range f.tenants {
		if role, ok := roleByTenant[t.ID]; ok {
			out = append(out, store.TenantWithRole{Tenant: *cloneTenant(t), Role: role})
		}
	}
	return out, nil
}

// --- projects (Phase 3) ---

func (f *fakeStore) CreateProject(_ context.Context, p store.CreateProjectParams) (*store.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ex := range f.projects {
		if ex.PoolID == p.PoolID || (ex.TenantID == p.TenantID && ex.Slug == p.Slug) {
			return nil, fmt.Errorf("create project: %w", store.ErrConflict)
		}
	}
	now := f.now()
	proj := &store.Project{
		ID: f.nextID("proj"), TenantID: p.TenantID, Name: p.Name, Slug: p.Slug,
		PoolID: p.PoolID, CreatedAt: now, UpdatedAt: now,
	}
	f.projects[proj.ID] = proj
	return cloneProject(proj), nil
}

func (f *fakeStore) GetProjectByID(_ context.Context, id string) (*store.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.projects[id]; ok {
		return cloneProject(p), nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) RenameProject(_ context.Context, id, name string) (*store.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	p.Name = name
	p.UpdatedAt = f.now()
	return cloneProject(p), nil
}

func (f *fakeStore) DeleteProject(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.projects, id)
	return nil
}

func (f *fakeStore) CountActiveOwnershipByProject(_ context.Context, projectID string) (int, error) {
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

// --- ownership (Phase 3) ---

func (f *fakeStore) GetOwnershipByVMID(_ context.Context, vmid int) (*store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if o, ok := f.ownership[vmid]; ok {
		return cloneOwnership(o), nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) CreateOwnership(_ context.Context, p store.CreateOwnershipParams) (*store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.ownership[p.VMID]; ok {
		return nil, fmt.Errorf("create ownership for vmid %d: %w", p.VMID, store.ErrConflict)
	}
	now := f.now()
	o := &store.ResourceOwnership{
		ID: f.nextID("own"), TenantID: p.TenantID, ProjectID: p.ProjectID, VMID: p.VMID,
		GuestType: p.GuestType, Node: p.Node, CreatedBy: p.CreatedBy, Status: p.Status,
		CreatedAt: now, UpdatedAt: now,
	}
	f.ownership[p.VMID] = o
	return cloneOwnership(o), nil
}

func (f *fakeStore) FinalizeOwnership(_ context.Context, id, upid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.ownership {
		if o.ID == id && o.Status == "pending" {
			o.Status = "active"
			u := upid
			o.PVEUPID = &u
			o.UpdatedAt = f.now()
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) ReleaseOwnership(_ context.Context, id string) error {
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

func (f *fakeStore) TombstoneOwnership(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, o := range f.ownership {
		if o.ID == id {
			o.Status = "tombstoned"
			o.UpdatedAt = f.now()
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) ListOwnershipByTenant(_ context.Context, tenantID string) ([]store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.ResourceOwnership{}
	for _, o := range f.ownership {
		if o.TenantID == tenantID {
			out = append(out, *cloneOwnership(o))
		}
	}
	return out, nil
}

func (f *fakeStore) ListOwnershipByProject(_ context.Context, projectID string) ([]store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.ResourceOwnership{}
	for _, o := range f.ownership {
		if o.ProjectID == projectID {
			out = append(out, *cloneOwnership(o))
		}
	}
	return out, nil
}

func (f *fakeStore) ListActiveVMIDs(context.Context) (map[int]bool, error) {
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

func (f *fakeStore) ListStalePendingOwnership(_ context.Context, olderThan time.Time) ([]store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []store.ResourceOwnership{}
	for _, o := range f.ownership {
		if o.Status == "pending" && o.CreatedAt.Before(olderThan) {
			out = append(out, *o)
		}
	}
	return out, nil
}

// --- quotas + reservation + audit (Phase 4) ---
//
// The auth package never exercises quotas; these are minimal stubs so fakeStore
// still satisfies store.Store. The real behavior is unit-tested via
// storetest.Fake and the Postgres integration tests.

func (f *fakeStore) GetQuota(context.Context, string, string) (*store.Quota, error) {
	return nil, store.ErrNotFound
}

func (f *fakeStore) UpsertQuota(_ context.Context, p store.UpsertQuotaParams) (*store.Quota, error) {
	return &store.Quota{
		ID: f.nextID("quota"), ScopeType: p.ScopeType, ScopeID: p.ScopeID,
		MaxVCPU: p.MaxVCPU, MaxRAMMB: p.MaxRAMMB, MaxDiskGB: p.MaxDiskGB, MaxCount: p.MaxCount,
	}, nil
}

func (f *fakeStore) ComputeUsage(context.Context, string, map[int]store.Alloc) (store.QuotaUsage, map[string]store.QuotaUsage, error) {
	return store.QuotaUsage{}, map[string]store.QuotaUsage{}, nil
}

func (f *fakeStore) ReserveOwnership(_ context.Context, p store.ReserveOwnershipParams) (*store.ResourceOwnership, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.ownership[p.VMID]; ok {
		return nil, fmt.Errorf("reserve ownership for vmid %d: %w", p.VMID, store.ErrConflict)
	}
	now := f.now()
	rv, rr, rd := p.Reserved.VCPU, p.Reserved.RAMMB, p.Reserved.DiskGB
	o := &store.ResourceOwnership{
		ID: f.nextID("own"), TenantID: p.TenantID, ProjectID: p.ProjectID, VMID: p.VMID,
		GuestType: p.GuestType, Node: p.Node, CreatedBy: p.CreatedBy, Status: "pending",
		ReservedVCPU: &rv, ReservedRAMMB: &rr, ReservedDiskGB: &rd, CreatedAt: now, UpdatedAt: now,
	}
	f.ownership[p.VMID] = o
	return cloneOwnership(o), nil
}

func (f *fakeStore) InsertAuditIntent(_ context.Context, a store.AuditIntent) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["InsertAuditIntent"]; err != nil {
		return "", err
	}
	id := f.nextID("audit")
	f.audit = append(f.audit, &store.AuditEntry{
		ID: id, TS: f.now(), ActorUserID: a.ActorUserID, TenantID: a.TenantID, ProjectID: a.ProjectID,
		Action: a.Action, TargetType: a.TargetType, TargetID: a.TargetID, Outcome: "pending", IP: a.IP,
	})
	return id, nil
}

func (f *fakeStore) FinalizeAudit(_ context.Context, id, outcome string, detail []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.fail["FinalizeAudit"]; err != nil {
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

func (f *fakeStore) ListAudit(context.Context, store.AuditQuery) ([]store.AuditEntry, error) {
	return nil, nil
}

// --- invitations, TOTP, recovery codes, login challenges (Phase 5) ---
//
// Functional in-memory doubles: chunk A does not exercise them, but the Phase-5
// TOTP/accept handlers (chunks B/C) live in this package and will, so single-use
// and supersede semantics are enforced here just like storetest.Fake.

func (f *fakeStore) CreateInvitation(_ context.Context, p store.CreateInvitationParams) (*store.Invitation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, inv := range f.invitations {
		if inv.AcceptedAt == nil && strings.EqualFold(inv.Email, p.Email) &&
			inv.ScopeType == p.ScopeType && inv.ScopeID == p.ScopeID {
			delete(f.invitations, id)
		}
	}
	for _, inv := range f.invitations {
		if inv.TokenHash == p.TokenHash {
			return nil, fmt.Errorf("create invitation: %w", store.ErrConflict)
		}
	}
	now := f.now()
	inv := &store.Invitation{
		ID: f.nextID("inv"), TokenHash: p.TokenHash, Email: p.Email, ScopeType: p.ScopeType,
		ScopeID: p.ScopeID, Role: p.Role, InvitedBy: p.InvitedBy, ExpiresAt: p.ExpiresAt,
		CreatedAt: now, UpdatedAt: now,
	}
	f.invitations[inv.ID] = inv
	c := *inv
	return &c, nil
}

func (f *fakeStore) GetInvitationByTokenHash(_ context.Context, tokenHash string) (*store.Invitation, error) {
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

func (f *fakeStore) GetInvitationByID(_ context.Context, id string) (*store.Invitation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if inv, ok := f.invitations[id]; ok {
		c := *inv
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) ListPendingInvitationsByScopes(_ context.Context, scopeType string, scopeIDs []string) ([]store.Invitation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	return out, nil
}

func (f *fakeStore) MarkInvitationAccepted(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inv, ok := f.invitations[id]
	if !ok || inv.AcceptedAt != nil {
		return false, nil
	}
	t := f.now()
	inv.AcceptedAt = &t
	inv.UpdatedAt = t
	return true, nil
}

func (f *fakeStore) DeleteInvitation(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.invitations[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.invitations, id)
	return nil
}

func (f *fakeStore) UpsertTOTPSecret(_ context.Context, userID string, secretEncrypted []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.totp[userID] = &store.TOTPSecret{
		UserID:          userID,
		SecretEncrypted: append([]byte(nil), secretEncrypted...),
		ConfirmedAt:     nil,
	}
	return nil
}

func (f *fakeStore) GetTOTPSecret(_ context.Context, userID string) (*store.TOTPSecret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.totp[userID]; ok {
		c := *t
		c.SecretEncrypted = append([]byte(nil), t.SecretEncrypted...)
		return &c, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) ConfirmTOTPSecret(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.totp[userID]
	if !ok || t.ConfirmedAt != nil {
		return store.ErrNotFound
	}
	now := f.now()
	t.ConfirmedAt = &now
	return nil
}

func (f *fakeStore) DeleteTOTPSecret(_ context.Context, userID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.totp, userID)
	return nil
}

func (f *fakeStore) ReplaceRecoveryCodes(_ context.Context, userID string, codeHashes []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
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

func (f *fakeStore) ConsumeRecoveryCode(_ context.Context, userID, codeHash string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, rc := range f.recovery {
		if rc.UserID == userID && rc.CodeHash == codeHash && rc.UsedAt == nil {
			t := f.now()
			rc.UsedAt = &t
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) CountUnusedRecoveryCodes(_ context.Context, userID string) (int, error) {
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

func (f *fakeStore) DeleteRecoveryCodes(_ context.Context, userID string) error {
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

func (f *fakeStore) CreateLoginChallenge(_ context.Context, p store.CreateLoginChallengeParams) (*store.LoginChallenge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.chalByHash[p.TokenHash]; ok {
		return nil, fmt.Errorf("create login challenge: %w", store.ErrConflict)
	}
	id := f.nextID("chal")
	lc := store.LoginChallenge{
		ID: id, UserID: p.UserID, Attempts: 0, CreatedAt: f.now(),
		ExpiresAt: p.ExpiresAt, IP: p.IP, UserAgent: p.UserAgent,
	}
	f.challenges[id] = &fakeChallenge{c: lc, tokenHash: p.TokenHash}
	f.chalByHash[p.TokenHash] = id
	c := lc
	return &c, nil
}

func (f *fakeStore) GetLoginChallengeByTokenHash(_ context.Context, tokenHash string) (*store.LoginChallenge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.chalByHash[tokenHash]
	if !ok {
		return nil, store.ErrNotFound
	}
	c := f.challenges[id].c
	return &c, nil
}

func (f *fakeStore) ConsumeLoginChallenge(_ context.Context, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.challenges[id]
	if !ok || ch.c.ConsumedAt != nil {
		return false, nil
	}
	t := f.now()
	ch.c.ConsumedAt = &t
	return true, nil
}

func (f *fakeStore) RecordChallengeFailure(_ context.Context, id string, maxAttempts int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch, ok := f.challenges[id]
	if !ok || ch.c.ConsumedAt != nil {
		return true, nil
	}
	ch.c.Attempts++
	if ch.c.Attempts >= maxAttempts {
		t := f.now()
		ch.c.ConsumedAt = &t
		return true, nil
	}
	return false, nil
}

// --- helpers ---

// fakeRoleRank mirrors store.roleRank (unexported there) for the fake's
// max-role reductions in ListTenantsForUser/GetEffectiveRoles.
func fakeRoleRank(role string) int {
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

func cloneTenant(t *store.Tenant) *store.Tenant {
	c := *t
	return &c
}

func cloneProject(p *store.Project) *store.Project {
	c := *p
	return &c
}

func cloneOwnership(o *store.ResourceOwnership) *store.ResourceOwnership {
	c := *o
	return &c
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func cloneUser(u *store.User) *store.User {
	c := *u
	return &c
}

func cloneSession(s *store.Session) *store.Session {
	c := *s
	return &c
}
