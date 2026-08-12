package auth

import (
	"strings"
	"testing"
)

func TestHasherArgon2idRoundTrip(t *testing.T) {
	h := NewHasher()
	encoded, err := h.Hash("correct-horse-battery")
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	if !strings.HasPrefix(encoded, "$argon2id$v=19$m=65536,t=3,p=2$") {
		t.Fatalf("unexpected PHC prefix: %q", encoded)
	}

	tests := []struct {
		name    string
		pw      string
		wantOK  bool
		wantReh bool
	}{
		{"correct password", "correct-horse-battery", true, false},
		{"wrong password", "wrong", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, needsRehash := h.Verify(tt.pw, encoded, AlgoArgon2id)
			if ok != tt.wantOK || needsRehash != tt.wantReh {
				t.Fatalf("Verify = (%v,%v), want (%v,%v)", ok, needsRehash, tt.wantOK, tt.wantReh)
			}
		})
	}
}

func TestHasherBcryptVerifyRequestsRehash(t *testing.T) {
	h := NewHasher()
	bhash, err := ResolveHash("", "legacy-password-123")
	if err != nil {
		t.Fatalf("ResolveHash: %v", err)
	}
	ok, needsRehash := h.Verify("legacy-password-123", bhash, AlgoBcrypt)
	if !ok || !needsRehash {
		t.Fatalf("bcrypt verify = (%v,%v), want (true,true)", ok, needsRehash)
	}
	if ok, _ := h.Verify("nope", bhash, AlgoBcrypt); ok {
		t.Fatal("bcrypt verify accepted a wrong password")
	}
}

func TestHasherRejectsMalformed(t *testing.T) {
	h := NewHasher()
	for _, enc := range []string{
		"", "not-a-hash", "$argon2id$bad", "$argon2i$v=19$m=65536,t=3,p=2$abc$def",
	} {
		if ok, _ := h.Verify("x", enc, AlgoArgon2id); ok {
			t.Fatalf("Verify accepted malformed encoded %q", enc)
		}
	}
	if ok, _ := h.Verify("x", "whatever", "unknown-algo"); ok {
		t.Fatal("Verify accepted an unknown algo")
	}
}

func TestHasherDistinctSaltsPerHash(t *testing.T) {
	h := NewHasher()
	a, _ := h.Hash("same")
	b, _ := h.Hash("same")
	if a == b {
		t.Fatal("two hashes of the same password are identical — salt not random")
	}
}
