package credential

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func newTestSealer(t *testing.T) *Sealer {
	t.Helper()

	key, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("generate master key: %v", err)
	}
	s, err := NewSealer(key)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	return s
}

func TestSealRoundTrip(t *testing.T) {
	t.Parallel()

	s := newTestSealer(t)

	for _, plaintext := range [][]byte{
		[]byte(`{"kind":"bearer","token":"secret"}`),
		[]byte(""),
		bytes.Repeat([]byte("x"), 8192),
	} {
		sealed, err := s.Seal(plaintext)
		if err != nil {
			t.Fatalf("seal: %v", err)
		}

		// The plaintext must not survive anywhere in the stored bytes.
		if len(plaintext) > 0 && bytes.Contains(sealed.Ciphertext, plaintext) {
			t.Fatal("ciphertext contains the plaintext")
		}

		got, err := s.Open(sealed)
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Errorf("round trip = %q, want %q", got, plaintext)
		}
	}
}

// Sealing the same plaintext twice must produce different ciphertext, or an
// observer could tell that two instances share a credential.
func TestSealIsNonDeterministic(t *testing.T) {
	t.Parallel()

	s := newTestSealer(t)
	plaintext := []byte("same input")

	first, err := s.Seal(plaintext)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	second, err := s.Seal(plaintext)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	if bytes.Equal(first.Ciphertext, second.Ciphertext) {
		t.Error("identical ciphertext for repeated seal")
	}
	if bytes.Equal(first.Nonce, second.Nonce) {
		t.Error("nonce reused across seals")
	}
}

// AES-GCM authenticates, so any modification must fail to open rather than
// yielding altered plaintext.
func TestOpenRejectsTampering(t *testing.T) {
	t.Parallel()

	s := newTestSealer(t)
	sealed, err := s.Seal([]byte("authentic"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Sealed)
	}{
		{"flip a ciphertext bit", func(x *Sealed) { x.Ciphertext[len(x.Ciphertext)-1] ^= 1 }},
		{"flip a nonce bit", func(x *Sealed) { x.Nonce[0] ^= 1 }},
		{"truncate ciphertext", func(x *Sealed) { x.Ciphertext = x.Ciphertext[:len(x.Ciphertext)-1] }},
		{"corrupt the wrapped key", func(x *Sealed) { x.Ciphertext[3] ^= 1 }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			damaged := &Sealed{
				Ciphertext: append([]byte{}, sealed.Ciphertext...),
				Nonce:      append([]byte{}, sealed.Nonce...),
				KeyID:      sealed.KeyID,
			}
			tc.mutate(damaged)

			if _, err := s.Open(damaged); err == nil {
				t.Error("tampered record opened successfully")
			}
		})
	}
}

// A record sealed under one master key must not open under another, and the
// failure must be distinguishable so rotation can detect it.
func TestOpenWithWrongKey(t *testing.T) {
	t.Parallel()

	sealer := newTestSealer(t)
	other := newTestSealer(t)

	sealed, err := sealer.Seal([]byte("secret"))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}

	_, err = other.Open(sealed)
	if !errors.Is(err, ErrWrongKey) {
		t.Fatalf("error = %v, want ErrWrongKey", err)
	}
}

// The key id identifies a key without revealing it.
func TestKeyID(t *testing.T) {
	t.Parallel()

	key, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	a, err := NewSealer(key)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}
	b, err := NewSealer(key)
	if err != nil {
		t.Fatalf("new sealer: %v", err)
	}

	if a.KeyID() != b.KeyID() {
		t.Error("same key produced different ids")
	}
	if strings.Contains(key, a.KeyID()) {
		t.Error("key id appears inside the key material")
	}
	if a.KeyID() == newTestSealer(t).KeyID() {
		t.Error("different keys produced the same id")
	}
}

func TestValidateMasterKey(t *testing.T) {
	t.Parallel()

	valid, err := GenerateMasterKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"generated key", valid, false},
		{"empty", "", true},
		{"not hex", strings.Repeat("z", 64), true},
		{"too short", strings.Repeat("ab", 16), true},
		{"too long", strings.Repeat("ab", 48), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateMasterKey(tc.key)
			if tc.wantErr && err == nil {
				t.Error("expected an error")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
