package catalog

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// MinPasswordLength is the length-only policy for a USER-SUPPLIED password
// (ADR-0027 §3): at least 12 characters, with NO composition/complexity rules.
// The mandatory base64 transport (§1) makes every byte safe to inject, so the
// policy governs strength, not escaping — a password may legally contain any
// shell/YAML metacharacter. Generated passwords clear this bar by construction.
const MinPasswordLength = 12

// usernameRe is the allowed charset for a user-SETTABLE credential username: an
// unquoted SQL-style identifier — a letter or underscore, then letters, digits,
// or underscores, at most 63 characters. This is a SEMANTIC guard (a sane DB
// role name), not a security boundary: injection safety comes from the base64
// pipeline (§1), never from this charset. A fixed-username credential (e.g.
// Postgres `postgres`) rejects a supplied username outright, before this runs.
var usernameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,62}$`)

// SuppliedCredential is one user-supplied credential value, keyed by the
// service's declared credential name. It is present only when the user chose
// "I'll set it" for that credential; a credential with no SuppliedCredential
// falls back to generation. A blank Username means "keep the definition's
// (fixed) username"; a blank Password is treated as "no password supplied" and
// rejected when an entry is otherwise present (see ResolveCredentials).
type SuppliedCredential struct {
	Name     string
	Username string
	Password string
}

// ResolvedCredential is the server-authoritative outcome for ONE declared
// credential: the final username + password to inject through the base64
// pipeline, and whether the PASSWORD came from the user. UserSupplied drives the
// audit boolean and the one-time reveal — a generated password is surfaced once,
// a user-supplied one is never echoed back.
type ResolvedCredential struct {
	Name         string
	Username     string
	Password     string
	UserSupplied bool
}

// CredentialError is a validation failure on user-supplied credential input. The
// provision handler maps it to a 400. Its message references only the credential
// NAME and the violated rule — NEVER the credential value.
type CredentialError struct{ Msg string }

func (e *CredentialError) Error() string { return e.Msg }

// GeneratePassword mints a strong, URL-safe secret with crypto/rand (18 random
// bytes → a 24-char string) that clears the length-only policy by construction.
// It is surfaced once in the provision response and never persisted, logged, or
// audited (ADR-0027 §2). It is the default generator passed to ResolveCredentials.
func GeneratePassword() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ResolveCredentials validates the user-supplied credentials against the
// service's declared specs and resolves each declared credential to a final
// username/password: user-supplied where given (and valid), generated otherwise.
// It is server-authoritative and MUST run before any VMID/quota reservation, so
// weak or malformed input is rejected before a guest is touched (mirrors the
// existing validate-before-reserve ordering).
//
// A *CredentialError (→ 400) is returned for any invalid user input; a non-
// CredentialError (→ 500) only for a crypto/rand failure inside gen. The raw
// values are used ONLY to build the base64 snippet transport (§1) and never
// appear in an error, a log line, or a store.
func ResolveCredentials(specs []CredentialSpec, supplied []SuppliedCredential, gen func() (string, error)) ([]ResolvedCredential, error) {
	// Index the user-supplied values by name. A name that matches no declared
	// credential is a typo that must fail loudly, not silently fall back to a
	// generated secret the user did not expect; a duplicate is ambiguous.
	declared := make(map[string]bool, len(specs))
	for _, s := range specs {
		declared[s.Name] = true
	}
	byName := make(map[string]SuppliedCredential, len(supplied))
	for _, sc := range supplied {
		name := strings.TrimSpace(sc.Name)
		if name == "" {
			return nil, &CredentialError{Msg: "a supplied credential is missing its name"}
		}
		if !declared[name] {
			return nil, &CredentialError{Msg: fmt.Sprintf("unknown credential %q for this service", name)}
		}
		if _, dup := byName[name]; dup {
			return nil, &CredentialError{Msg: fmt.Sprintf("credential %q supplied more than once", name)}
		}
		byName[name] = sc
	}

	out := make([]ResolvedCredential, 0, len(specs))
	for _, spec := range specs {
		var sc *SuppliedCredential
		if v, ok := byName[spec.Name]; ok {
			sc = &v
		}
		rc, err := resolveOne(spec, sc, gen)
		if err != nil {
			return nil, err
		}
		out = append(out, rc)
	}
	return out, nil
}

// resolveOne resolves a single declared credential. sc is nil when the user
// supplied nothing for it (→ generate). A present sc means the user is supplying
// the credential, so its password is required and validated length-only.
func resolveOne(spec CredentialSpec, sc *SuppliedCredential, gen func() (string, error)) (ResolvedCredential, error) {
	// The definition's fixed username (falling back to the credential name).
	username := strings.TrimSpace(spec.Username)
	if username == "" {
		username = spec.Name
	}

	// No entry → generate (the Phase A path), keeping the fixed username.
	if sc == nil {
		if !spec.GeneratedDefault {
			return ResolvedCredential{}, &CredentialError{Msg: fmt.Sprintf("credential %q must be supplied (this service has no generated default)", spec.Name)}
		}
		pw, err := gen()
		if err != nil {
			return ResolvedCredential{}, err // non-CredentialError → 500
		}
		return ResolvedCredential{Name: spec.Name, Username: username, Password: pw, UserSupplied: false}, nil
	}

	// An entry is present → the user is supplying this credential.
	if !spec.UserSettable {
		return ResolvedCredential{}, &CredentialError{Msg: fmt.Sprintf("credential %q cannot be user-supplied for this service", spec.Name)}
	}

	// Username: accepted only when the credential's username is settable. Postgres
	// fixes `postgres`, so a supplied username there is rejected here — before any
	// reservation — with a clear message (the name is not a secret).
	if u := strings.TrimSpace(sc.Username); u != "" {
		if !spec.UsernameSettable {
			return ResolvedCredential{}, &CredentialError{Msg: fmt.Sprintf("the username for credential %q is fixed (%q) and cannot be set", spec.Name, username)}
		}
		if !usernameRe.MatchString(u) {
			return ResolvedCredential{}, &CredentialError{Msg: fmt.Sprintf("username for credential %q must start with a letter or underscore and contain only letters, digits, and underscores (max 63)", spec.Name)}
		}
		username = u
	}

	// Password: length-only policy (≥ MinPasswordLength). NO composition rules —
	// metacharacters are legal because §1 makes every byte safe to transport. An
	// empty password (the user engaged the field but sent nothing) is 0 < 12 and is
	// rejected here, before any reservation. The password is NOT trimmed: leading/
	// trailing whitespace is part of the secret.
	if utf8.RuneCountInString(sc.Password) < MinPasswordLength {
		return ResolvedCredential{}, &CredentialError{Msg: fmt.Sprintf("password for credential %q must be at least %d characters", spec.Name, MinPasswordLength)}
	}
	return ResolvedCredential{Name: spec.Name, Username: username, Password: sc.Password, UserSupplied: true}, nil
}
