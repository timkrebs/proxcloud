package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters (OWASP-recommended baseline, ADR-0006): 64 MiB memory,
// 3 iterations, 2 lanes, 16-byte salt, 32-byte key. Encoded into every hash so
// a future parameter bump self-describes and can drive a rehash.
const (
	argon2Memory  uint32 = 64 * 1024 // KiB → 64 MiB
	argon2Time    uint32 = 3
	argon2Threads uint8  = 2
	argon2SaltLen        = 16
	argon2KeyLen  uint32 = 32
)

// Password algorithm identifiers stored in users.password_algo.
const (
	AlgoArgon2id = "argon2id"
	AlgoBcrypt   = "bcrypt"
)

// PasswordHasher hashes and verifies passwords. Argon2id is the current
// algorithm; legacy bcrypt hashes (the migrated env admin) are verified and
// flagged for transparent rehash to Argon2id on the next successful login.
type PasswordHasher struct {
	// dummyHash is a real Argon2id hash of a random secret, verified in the
	// unknown-user branch so login timing does not reveal whether an email
	// exists (the verify does the same memory-hard work either way).
	dummyHash string
}

// NewHasher returns a ready hasher with a precomputed dummy hash for timing
// flattening.
func NewHasher() *PasswordHasher {
	h := &PasswordHasher{}
	filler := make([]byte, 32)
	_, _ = rand.Read(filler)
	dummy, err := h.Hash(base64.RawStdEncoding.EncodeToString(filler))
	if err != nil {
		// Hash only fails if crypto/rand fails; fall back to a fixed valid PHC
		// string so VerifyDummy still does argon2 work.
		dummy = "$argon2id$v=19$m=65536,t=3,p=2$" +
			"AAAAAAAAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	}
	h.dummyHash = dummy
	return h
}

// Hash returns an Argon2id PHC-encoded string for pw.
func (h *PasswordHasher) Hash(pw string) (string, error) {
	salt := make([]byte, argon2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("auth: generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(pw), salt, argon2Time, argon2Memory, argon2Threads, argon2KeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2Memory, argon2Time, argon2Threads,
		b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// Verify reports whether pw matches the encoded hash produced under algo, and
// whether the hash should be rehashed to the current Argon2id parameters.
// A bcrypt match always requests a rehash (cutover to Argon2id).
func (h *PasswordHasher) Verify(pw, encoded, algo string) (ok, needsRehash bool) {
	switch algo {
	case AlgoBcrypt:
		return CheckPassword(encoded, pw), true
	case AlgoArgon2id:
		return verifyArgon2id(pw, encoded), false
	default:
		return false, false
	}
}

// VerifyDummy runs an Argon2id verification against the precomputed dummy hash
// and discards the result. Called in the unknown-user login branch so response
// timing does not distinguish a missing email from a wrong password.
func (h *PasswordHasher) VerifyDummy(pw string) {
	_ = verifyArgon2id(pw, h.dummyHash)
}

// verifyArgon2id parses a PHC Argon2id string, recomputes the derived key with
// the embedded parameters and salt, and compares in constant time.
func verifyArgon2id(pw, encoded string) bool {
	mem, iter, par, salt, hash, err := parseArgon2id(encoded)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, iter, mem, par, uint32(len(hash)))
	return subtle.ConstantTimeCompare(got, hash) == 1
}

// parseArgon2id decodes the standard PHC string
// $argon2id$v=19$m=<mem>,t=<time>,p=<par>$<b64salt>$<b64hash>.
func parseArgon2id(encoded string) (mem, iter uint32, par uint8, salt, hash []byte, err error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", "<salt>", "<hash>"
	if len(parts) != 6 || parts[1] != "argon2id" {
		return 0, 0, 0, nil, nil, errors.New("auth: malformed argon2id hash")
	}
	var version int
	if _, err = fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return 0, 0, 0, nil, nil, errors.New("auth: unsupported argon2 version")
	}
	var p int
	if _, err = fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iter, &p); err != nil {
		return 0, 0, 0, nil, nil, errors.New("auth: malformed argon2id params")
	}
	par = uint8(p)
	b64 := base64.RawStdEncoding
	if salt, err = b64.DecodeString(parts[4]); err != nil {
		return 0, 0, 0, nil, nil, errors.New("auth: malformed argon2id salt")
	}
	if hash, err = b64.DecodeString(parts[5]); err != nil {
		return 0, 0, 0, nil, nil, errors.New("auth: malformed argon2id hash")
	}
	return mem, iter, par, salt, hash, nil
}
