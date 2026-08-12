package authz

// Role is the ordered tenancy role type. authz owns the canonical ordering and
// compare so auth/store stay free of authz (the dependency is one-way:
// authz imports auth+store, never the reverse). Roles are carried as plain
// strings on auth.Identity; ParseRole lifts them into this comparable type.
type Role int

const (
	// RoleNone is "no role in scope" — the zero value; below every real role.
	RoleNone Role = iota
	// RoleReader can view resources and metrics.
	RoleReader
	// RoleContributor can CRUD resources (implies Reader).
	RoleContributor
	// RoleOwner has full control incl. member/project management (implies all).
	RoleOwner
)

// role string constants, matching the DB CHECK vocabulary and auth.Identity.
const (
	RoleOwnerStr       = "owner"
	RoleContributorStr = "contributor"
	RoleReaderStr      = "reader"
)

// ParseRole maps a role string ("owner"|"contributor"|"reader"|"") to a Role.
func ParseRole(s string) Role {
	switch s {
	case RoleOwnerStr:
		return RoleOwner
	case RoleContributorStr:
		return RoleContributor
	case RoleReaderStr:
		return RoleReader
	default:
		return RoleNone
	}
}

// String renders a Role back to its canonical string.
func (r Role) String() string {
	switch r {
	case RoleOwner:
		return RoleOwnerStr
	case RoleContributor:
		return RoleContributorStr
	case RoleReader:
		return RoleReaderStr
	default:
		return ""
	}
}

// RoleAtLeast reports whether have satisfies the need threshold
// (owner > contributor > reader > none).
func RoleAtLeast(have, need Role) bool {
	return have >= need
}

// MaxRole returns the higher of two role strings — the effective role when a
// project-scope membership adds privilege on top of a tenant-scope one. A
// project role can only add, never subtract (ADR-0007).
func MaxRole(a, b string) string {
	if ParseRole(a) >= ParseRole(b) {
		return a
	}
	return b
}

// RuleMinRole maps a Rule to the minimum Role that satisfies it. Non-role rules
// (Public/Authenticated/PlatformAdmin) map to RoleNone — they are enforced by a
// different gate (the public router surface, Authenticate, RequirePlatformAdmin),
// not by the tenant-scope role comparison in Enforce.
func RuleMinRole(rule Rule) Role {
	switch rule {
	case ReaderRule:
		return RoleReader
	case ContributorRule:
		return RoleContributor
	case OwnerRule:
		return RoleOwner
	default:
		return RoleNone
	}
}
