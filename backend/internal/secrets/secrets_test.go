package secrets

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func newTestKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand key: %v", err)
	}
	return key
}

func TestNewKeyLength(t *testing.T) {
	tests := []struct {
		name    string
		keyLen  int
		wantErr bool
	}{
		{name: "correct 32 bytes", keyLen: 32, wantErr: false},
		{name: "too short", keyLen: 16, wantErr: true},
		{name: "too long", keyLen: 33, wantErr: true},
		{name: "empty", keyLen: 0, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(make([]byte, tc.keyLen))
			if (err != nil) != tc.wantErr {
				t.Fatalf("New(len=%d) err = %v, wantErr = %v", tc.keyLen, err, tc.wantErr)
			}
		})
	}
}

func TestSealOpenRoundTrip(t *testing.T) {
	c, err := New(newTestKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	tests := []struct {
		name      string
		plaintext []byte
	}{
		{name: "empty", plaintext: []byte{}},
		{name: "short", plaintext: []byte("JBSWY3DPEHPK3PXP")},
		{name: "binary", plaintext: []byte{0x00, 0xff, 0x10, 0x42, 0x00}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			blob := c.Seal(tc.plaintext)
			got, err := c.Open(blob)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(got, tc.plaintext) {
				t.Fatalf("round-trip = %x, want %x", got, tc.plaintext)
			}
			// The plaintext must NOT appear in the ciphertext (encrypted at rest).
			if len(tc.plaintext) > 0 && bytes.Contains(blob, tc.plaintext) {
				t.Fatalf("plaintext leaked into sealed blob")
			}
		})
	}
}

func TestSealRandomNonce(t *testing.T) {
	c, err := New(newTestKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	pt := []byte("same input every time")
	a := c.Seal(pt)
	b := c.Seal(pt)
	if bytes.Equal(a, b) {
		t.Fatal("two Seals of the same plaintext are identical — nonce is not random")
	}
	// Both must still open to the same plaintext.
	for _, blob := range [][]byte{a, b} {
		got, err := c.Open(blob)
		if err != nil || !bytes.Equal(got, pt) {
			t.Fatalf("Open = (%q,%v), want (%q,nil)", got, err, pt)
		}
	}
}

func TestOpenTamperFailsClosed(t *testing.T) {
	c, err := New(newTestKey(t))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	blob := c.Seal([]byte("authenticated secret"))

	// A flipped byte anywhere (nonce, ciphertext, or tag) must fail Open.
	for i := range blob {
		tampered := append([]byte(nil), blob...)
		tampered[i] ^= 0x01
		if _, err := c.Open(tampered); err == nil {
			t.Fatalf("Open accepted tampered blob (flipped byte %d)", i)
		}
	}

	// A truncated blob (shorter than the nonce) is rejected, not panicked.
	if _, err := c.Open(blob[:1]); err == nil {
		t.Fatal("Open accepted a truncated blob")
	}
}

func TestOpenWrongKey(t *testing.T) {
	c1, _ := New(newTestKey(t))
	c2, _ := New(newTestKey(t))
	blob := c1.Seal([]byte("secret"))
	if _, err := c2.Open(blob); err == nil {
		t.Fatal("Open with a different key succeeded — AEAD did not fail closed")
	}
}
