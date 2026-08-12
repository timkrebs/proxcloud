// Package secrets provides authenticated symmetric encryption for the one
// secret Proxcloud stores at rest: the TOTP shared secret (ADR-0013 §2). A
// Cipher wraps SECRETS_KEY (32 bytes, AES-256) in GCM (AEAD) so a tampered
// ciphertext fails closed on Open rather than decrypting to garbage. The nonce
// is random per Seal and prepended to the ciphertext, so callers store a single
// opaque blob and never manage nonces themselves.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
)

// KeySize is the required SECRETS_KEY length in bytes (AES-256).
const KeySize = 32

// Cipher seals and opens secrets with AES-256-GCM. It is safe for concurrent
// use: the underlying cipher.AEAD is stateless across Seal/Open calls.
type Cipher struct {
	aead cipher.AEAD
}

// New constructs a Cipher from a 32-byte key. A key of any other length is a
// configuration error (the caller validates SECRETS_KEY at boot; this is the
// last-line guard).
func New(key []byte) (*Cipher, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("secrets: key must be %d bytes, got %d", KeySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("secrets: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secrets: new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Seal encrypts plaintext and returns nonce(12B) ‖ ciphertext‖tag as one opaque
// blob. A fresh random nonce is drawn per call, so two Seals of the same input
// differ. The result is what callers persist (e.g. totp_secrets.secret_encrypted).
//
// It panics only if the OS CSPRNG (crypto/rand) fails, which indicates a broken
// system and is unrecoverable — the AEAD contract cannot be honored with a
// non-random nonce, and the signature deliberately carries no error.
func (c *Cipher) Seal(plaintext []byte) []byte {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		panic(fmt.Errorf("secrets: read nonce from crypto/rand: %w", err))
	}
	// Prepend the nonce; Seal appends ciphertext+tag to the nonce prefix.
	return c.aead.Seal(nonce, nonce, plaintext, nil)
}

// Open reverses Seal: it splits the prepended nonce, verifies the GCM auth tag,
// and returns the plaintext. Any tampering (flipped byte in nonce, ciphertext,
// or tag) or a truncated blob returns an error and no plaintext (fail-closed).
func (c *Cipher) Open(blob []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	if len(blob) < ns {
		return nil, errors.New("secrets: ciphertext too short")
	}
	nonce, ciphertext := blob[:ns], blob[ns:]
	plaintext, err := c.aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("secrets: open: %w", err)
	}
	return plaintext, nil
}
