package gateway

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cainydev/tine/internal/credential"
)

// SignatureParam carries a signed URL's proof in the query string.
//
// The MCP endpoint is matched as POST /{user}/{integration}/{id}, so a proof
// cannot occupy a path segment without changing what an endpoint is.
const SignatureParam = "k"

// signedURLInfo is the HKDF context separating URL signing from every other use
// of the master key.
const signedURLInfo = "tine signed url v1"

// Signer mints and verifies signed endpoint URLs.
//
// A signed URL authenticates by possession: whoever holds it acts as the
// subject named inside it, until it expires. Nothing is stored, so expiry is the
// only revocation, and the lifetime should stay short.
type Signer struct {
	key []byte
}

// NewSigner derives a URL signing key from the hex-encoded master key.
//
// The signing key is derived rather than used directly so that a signature can
// never be confused with, or help attack, a sealed credential.
func NewSigner(hexMasterKey string) (*Signer, error) {
	master, err := decodeKey(hexMasterKey)
	if err != nil {
		return nil, err
	}

	key, err := hkdf.Key(sha256.New, master, nil, signedURLInfo, sha256.Size)
	if err != nil {
		return nil, fmt.Errorf("derive url signing key: %w", err)
	}
	return &Signer{key: key}, nil
}

// Sign returns the query value proving the bearer may reach path as subject
// until expiry.
//
// The path is the instance path, as /user/integration/id. It is covered by the
// signature so a proof minted for one instance cannot be replayed against
// another.
func (s *Signer) Sign(path, subject string, expiry time.Time) string {
	payload := signedPayload(path, subject, expiry.Unix())
	mac := s.mac(payload)

	return encodeSegment(payload) + "." + encodeSegment(mac)
}

// SignedURL returns endpoint with a proof appended.
func (s *Signer) SignedURL(endpoint, path, subject string, expiry time.Time) string {
	sep := "?"
	if strings.Contains(endpoint, "?") {
		sep = "&"
	}
	return endpoint + sep + SignatureParam + "=" + s.Sign(path, subject, expiry)
}

// Verify checks a proof against a path and returns the subject it names.
func (s *Signer) Verify(token, path string, now time.Time) (string, error) {
	rawPayload, rawMAC, ok := strings.Cut(token, ".")
	if !ok {
		return "", fmt.Errorf("%w: malformed signature", ErrUnauthenticated)
	}

	payload, err := decodeSegment(rawPayload)
	if err != nil {
		return "", fmt.Errorf("%w: malformed payload", ErrUnauthenticated)
	}
	mac, err := decodeSegment(rawMAC)
	if err != nil {
		return "", fmt.Errorf("%w: malformed signature", ErrUnauthenticated)
	}

	// Compared before parsing, so nothing downstream ever reads an unverified
	// payload.
	if !hmac.Equal(mac, s.mac(payload)) {
		return "", fmt.Errorf("%w: signature does not verify", ErrUnauthenticated)
	}

	subject, signedPath, expiry, err := parsePayload(payload)
	if err != nil {
		return "", err
	}
	if signedPath != path {
		return "", fmt.Errorf("%w: signature covers %q, not %q", ErrUnauthenticated, signedPath, path)
	}
	if now.Unix() >= expiry {
		return "", fmt.Errorf("%w: signature expired", ErrUnauthenticated)
	}
	return subject, nil
}

func (s *Signer) mac(payload []byte) []byte {
	m := hmac.New(sha256.New, s.key)
	m.Write(payload)
	return m.Sum(nil)
}

// signedPayload encodes what a signature covers.
//
// Fields are length-prefixed so no subject or path can be crafted to shift a
// boundary and have one field read as another.
func signedPayload(path, subject string, expiry int64) []byte {
	var b strings.Builder

	fmt.Fprintf(&b, "%d:%s", len(subject), subject)
	fmt.Fprintf(&b, "%d:%s", len(path), path)
	fmt.Fprintf(&b, "%d", expiry)

	return []byte(b.String())
}

func parsePayload(payload []byte) (subject, path string, expiry int64, err error) {
	rest := string(payload)

	subject, rest, err = readField(rest)
	if err != nil {
		return "", "", 0, err
	}
	path, rest, err = readField(rest)
	if err != nil {
		return "", "", 0, err
	}

	expiry, err = strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return "", "", 0, fmt.Errorf("%w: malformed expiry", ErrUnauthenticated)
	}
	return subject, path, expiry, nil
}

func readField(s string) (value, rest string, err error) {
	rawLen, rest, ok := strings.Cut(s, ":")
	if !ok {
		return "", "", fmt.Errorf("%w: malformed payload", ErrUnauthenticated)
	}

	n, err := strconv.Atoi(rawLen)
	if err != nil || n < 0 || n > len(rest) {
		return "", "", fmt.Errorf("%w: malformed payload", ErrUnauthenticated)
	}
	return rest[:n], rest[n:], nil
}

func encodeSegment(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeSegment(s string) ([]byte, error) {
	out, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("decode segment: %w", err)
	}
	return out, nil
}

// signedURLAuthenticator accepts a valid signed URL and otherwise falls back to
// the authenticator it wraps.
//
// It wraps rather than replaces because both must work at once: an agent
// launched with a signed URL and an MCP client running the OAuth flow reach the
// same endpoints on the same server.
type signedURLAuthenticator struct {
	signer *Signer
	next   Authenticator
	now    func() time.Time
}

// WithSignedURLs returns an Authenticator that additionally accepts signed URLs.
func WithSignedURLs(next Authenticator, signer *Signer) Authenticator {
	return &signedURLAuthenticator{signer: signer, next: next, now: time.Now}
}

func (a *signedURLAuthenticator) Authenticate(ctx context.Context, r *http.Request) (string, error) {
	token := r.URL.Query().Get(SignatureParam)
	if token == "" {
		return a.next.Authenticate(ctx, r)
	}

	subject, err := a.signer.Verify(token, r.URL.Path, a.now())
	if err != nil {
		return "", err
	}
	return subject, nil
}

// challenge delegates, so a client presenting no signature still learns where to
// authenticate.
func (a *signedURLAuthenticator) challenge(w http.ResponseWriter) { a.next.challenge(w) }

func (a *signedURLAuthenticator) metadata() protectedResourceMetadata { return a.next.metadata() }

func decodeKey(hexKey string) ([]byte, error) {
	if err := credential.ValidateMasterKey(hexKey); err != nil {
		return nil, fmt.Errorf("master key: %w", err)
	}

	key, err := hex.DecodeString(hexKey)
	if err != nil {
		return nil, fmt.Errorf("decode master key: %w", err)
	}
	return key, nil
}
