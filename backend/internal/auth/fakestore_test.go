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
	tenants     map[string]*store.Tenant // by slug

	now func() time.Time
}

func newFakeStore() *fakeStore {
	f := &fakeStore{
		users:       map[string]*store.User{},
		sessions:    map[string]*store.Session{},
		memberships: map[string]*store.Membership{},
		tenants:     map[string]*store.Tenant{},
		now:         time.Now,
	}
	f.tenants["default"] = &store.Tenant{ID: "tenant-default", Name: "Default", Slug: "default"}
	return f
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
	for _, u := range f.users {
		if strings.EqualFold(u.Email, p.Email) {
			return nil, fmt.Errorf("duplicate email")
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

// --- helpers ---

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
