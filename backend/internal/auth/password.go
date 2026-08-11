package auth

import (
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// ResolveHash returns the bcrypt hash to verify logins against. Production
// sets ADMIN_PASSWORD_HASH; for dev convenience a plaintext ADMIN_PASSWORD
// is hashed at boot so the plaintext never lives beyond startup.
func ResolveHash(hash, plaintext string) (string, error) {
	if hash != "" {
		if _, err := bcrypt.Cost([]byte(hash)); err != nil {
			return "", fmt.Errorf("ADMIN_PASSWORD_HASH is not a valid bcrypt hash: %w", err)
		}
		return hash, nil
	}
	h, err := bcrypt.GenerateFromPassword([]byte(plaintext), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hashing ADMIN_PASSWORD: %w", err)
	}
	return string(h), nil
}

// CheckPassword reports whether plaintext matches the bcrypt hash.
func CheckPassword(hash, plaintext string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(plaintext)) == nil
}
