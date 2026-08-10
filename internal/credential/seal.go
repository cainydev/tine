package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

// MasterKeySize is the required length of a decoded master key.
const MasterKeySize = 32

// ErrWrongKey is returned when a record cannot be opened with the key it names.
var ErrWrongKey = errors.New("credential sealed with a different key")

// Sealed is an encrypted credential payload as stored.
type Sealed struct {
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
}

// Sealer seals and opens credential payloads under a master key.
type Sealer struct {
	masterKey []byte
	keyID     string
}

// NewSealer returns a Sealer for the hex-encoded master key.
func NewSealer(hexKey string) (*Sealer, error) {
	key, err := decodeMasterKey(hexKey)
	if err != nil {
		return nil, err
	}
	return &Sealer{masterKey: key, keyID: keyIDFor(key)}, nil
}

// KeyID identifies the master key currently in use. It is stored alongside each
// record so rotation can find rows still sealed under an older key.
func (s *Sealer) KeyID() string { return s.keyID }

// Seal encrypts plaintext under a fresh data key.
func (s *Sealer) Seal(plaintext []byte) (*Sealed, error) {
	dataKey := make([]byte, MasterKeySize)
	if _, err := io.ReadFull(rand.Reader, dataKey); err != nil {
		return nil, fmt.Errorf("generate data key: %w", err)
	}

	payloadAEAD, err := newAEAD(dataKey)
	if err != nil {
		return nil, err
	}
	payloadNonce, err := randomNonce(payloadAEAD.NonceSize())
	if err != nil {
		return nil, err
	}
	payload := payloadAEAD.Seal(nil, payloadNonce, plaintext, nil)

	masterAEAD, err := newAEAD(s.masterKey)
	if err != nil {
		return nil, err
	}
	wrapNonce, err := randomNonce(masterAEAD.NonceSize())
	if err != nil {
		return nil, err
	}
	wrappedKey := masterAEAD.Seal(nil, wrapNonce, dataKey, nil)

	out := make([]byte, 0, 2+len(wrappedKey)+len(payload))
	out = append(out, byte(len(wrappedKey)>>8), byte(len(wrappedKey)))
	out = append(out, wrappedKey...)
	out = append(out, payload...)

	return &Sealed{
		Ciphertext: out,
		Nonce:      append(append([]byte{}, wrapNonce...), payloadNonce...),
		KeyID:      s.keyID,
	}, nil
}

// Open decrypts a sealed record.
func (s *Sealer) Open(sealed *Sealed) ([]byte, error) {
	if sealed.KeyID != s.keyID {
		return nil, fmt.Errorf("%w: record uses %q, sealer holds %q", ErrWrongKey, sealed.KeyID, s.keyID)
	}

	masterAEAD, err := newAEAD(s.masterKey)
	if err != nil {
		return nil, err
	}
	nonceSize := masterAEAD.NonceSize()

	if len(sealed.Nonce) != nonceSize*2 {
		return nil, fmt.Errorf("malformed record: nonce is %d bytes, want %d", len(sealed.Nonce), nonceSize*2)
	}
	wrapNonce, payloadNonce := sealed.Nonce[:nonceSize], sealed.Nonce[nonceSize:]

	if len(sealed.Ciphertext) < 2 {
		return nil, errors.New("malformed record: ciphertext too short for length prefix")
	}
	wrappedLen := int(sealed.Ciphertext[0])<<8 | int(sealed.Ciphertext[1])
	if len(sealed.Ciphertext) < 2+wrappedLen {
		return nil, errors.New("malformed record: wrapped key length exceeds ciphertext")
	}
	wrappedKey := sealed.Ciphertext[2 : 2+wrappedLen]
	payload := sealed.Ciphertext[2+wrappedLen:]

	dataKey, err := masterAEAD.Open(nil, wrapNonce, wrappedKey, nil)
	if err != nil {
		return nil, fmt.Errorf("unwrap data key: %w", err)
	}

	payloadAEAD, err := newAEAD(dataKey)
	if err != nil {
		return nil, err
	}
	plaintext, err := payloadAEAD.Open(nil, payloadNonce, payload, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt payload: %w", err)
	}
	return plaintext, nil
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("new gcm: %w", err)
	}
	return aead, nil
}

func randomNonce(size int) ([]byte, error) {
	nonce := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}
	return nonce, nil
}

// keyIDFor derives a stable, non-secret identifier for a key.
func keyIDFor(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

func decodeMasterKey(hexKey string) ([]byte, error) {
	if hexKey == "" {
		return nil, errors.New("master key is empty")
	}
	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("master key is not valid hex: %w", err)
	}
	if len(key) != MasterKeySize {
		return nil, fmt.Errorf("master key is %d bytes, want %d", len(key), MasterKeySize)
	}
	return key, nil
}

// ValidateMasterKey reports whether a hex-encoded master key is usable.
func ValidateMasterKey(hexKey string) error {
	_, err := decodeMasterKey(hexKey)
	return err
}

// GenerateMasterKey returns a new hex-encoded master key.
func GenerateMasterKey() (string, error) {
	key := make([]byte, MasterKeySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", fmt.Errorf("generate master key: %w", err)
	}
	return hex.EncodeToString(key), nil
}
