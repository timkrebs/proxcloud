// Package storetest provides an in-memory store.Store for table-driven tenancy
// and authz tests — the store-side mirror of proxmoxtest.MockClient. It is a
// faithful behavioral double (tenant/project filters, ownership status,
// unique-VMID reservation) so tests exercise the real enforcement chain and
// handlers against realistic data without a Postgres.
package storetest

import (
	"context"
	"fmt"
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
}

var _ store.Store = (*Fake)(nil)

// New returns an empty Fake.
func New() *Fake {
	return &Fake{
		Now:         time.Now,
		fail:        map[string]error{},
		users:       map[string]*store.User{},
		sessions:    map[string]*store.Session{},
		memberships: map[string]*store.Membership{},
		tenants:     map[string]*store.Tenant{},
		projects:    map[string]*store.Project{},
		ownership:   map[int]*store.ResourceOwnership{},
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
func (f *Fake) RunMigrations() (uint, error)              { return 2, nil }
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
	f.mu.Unlock()

	if err := fn(f); err != nil {
		f.mu.Lock()
		f.tenants = tenants
		f.projects = projects
		f.ownership = ownership
		f.memberships = memberships
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
	for _, u := range f.users {
		if strings.EqualFold(u.Email, p.Email) {
			return nil, fmt.Errorf("duplicate email")
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
	// Mirror the UNIQUE(vmid) constraint: a duplicate reservation is a conflict.
	if _, ok := f.ownership[p.VMID]; ok {
		return nil, fmt.Errorf("create ownership for vmid %d: %w", p.VMID, store.ErrConflict)
	}
	id := f.next("own")
	o := &store.ResourceOwnership{ID: id, TenantID: p.TenantID, ProjectID: p.ProjectID, VMID: p.VMID, GuestType: p.GuestType, Node: p.Node, CreatedBy: p.CreatedBy, Status: p.Status, CreatedAt: f.Now(), UpdatedAt: f.Now()}
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
