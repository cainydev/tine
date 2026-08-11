package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testMasterKey is 32 bytes hex-encoded.
const (
	testMasterKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	otherKey      = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func testSigner(t *testing.T, hexKey string) *Signer {
	t.Helper()

	s, err := NewSigner(hexKey)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	return s
}

func TestNewSignerRejectsBadKeys(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		key  string
	}{
		{"empty", ""},
		{"not hex", strings.Repeat("z", 64)},
		{"too short", "0123456789abcdef"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NewSigner(tc.key); err == nil {
				t.Fatal("want an error, got nil")
			}
		})
	}
}

func TestSignAndVerify(t *testing.T) {
	t.Parallel()

	const (
		path    = "/john/shopware/edc1e8b0"
		subject = "user-123"
	)

	now := time.Unix(1_700_000_000, 0)
	signer := testSigner(t, testMasterKey)
	token := signer.Sign(path, subject, now.Add(time.Hour))

	got, err := signer.Verify(token, path, now)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if got != subject {
		t.Errorf("subject = %q, want %q", got, subject)
	}
}

func TestVerifyRejects(t *testing.T) {
	t.Parallel()

	const (
		path    = "/john/shopware/edc1e8b0"
		subject = "user-123"
	)

	now := time.Unix(1_700_000_000, 0)
	signer := testSigner(t, testMasterKey)
	valid := signer.Sign(path, subject, now.Add(time.Hour))

	tests := []struct {
		name  string
		token string
		path  string
		now   time.Time
	}{
		{
			name:  "expired",
			token: valid,
			path:  path,
			now:   now.Add(2 * time.Hour),
		},
		{
			name:  "exactly at expiry",
			token: valid,
			path:  path,
			now:   now.Add(time.Hour),
		},
		{
			name:  "another instance",
			token: valid,
			path:  "/john/shopware/deadbeef",
			now:   now,
		},
		{
			name:  "another user",
			token: valid,
			path:  "/mallory/shopware/edc1e8b0",
			now:   now,
		},
		{
			name:  "signed with a different key",
			token: testSigner(t, otherKey).Sign(path, subject, now.Add(time.Hour)),
			path:  path,
			now:   now,
		},
		{
			name:  "tampered payload",
			token: tamper(valid),
			path:  path,
			now:   now,
		},
		{
			name:  "no separator",
			token: "notasignature",
			path:  path,
			now:   now,
		},
		{
			name:  "empty",
			token: "",
			path:  path,
			now:   now,
		},
		{
			name:  "payload not base64",
			token: "!!!.###",
			path:  path,
			now:   now,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := signer.Verify(tc.token, tc.path, tc.now)
			if err == nil {
				t.Fatalf("Verify accepted %q, returned subject %q", tc.name, got)
			}
			if !errors.Is(err, ErrUnauthenticated) {
				t.Errorf("error = %v, want it to wrap ErrUnauthenticated", err)
			}
		})
	}
}

// TestVerifyRejectsFieldBoundaryShift covers the reason the payload is
// length-prefixed: a subject that looks like a field header must not be able to
// move the boundary and claim another path.
func TestVerifyRejectsFieldBoundaryShift(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	signer := testSigner(t, testMasterKey)

	const attacker = "mallory:24:/john/shopware/edc1e8b0"

	token := signer.Sign("/mallory/shopware/aaaa", attacker, now.Add(time.Hour))

	if _, err := signer.Verify(token, "/john/shopware/edc1e8b0", now); err == nil {
		t.Fatal("a crafted subject reached another user's instance")
	}
}

func TestSignedURL(t *testing.T) {
	t.Parallel()

	now := time.Unix(1_700_000_000, 0)
	signer := testSigner(t, testMasterKey)

	const (
		base = "https://tine.cainy.dev"
		path = "/john/shopware/edc1e8b0"
	)

	url := signer.SignedURL(base+path, path, "user-123", now.Add(time.Hour))

	if !strings.HasPrefix(url, base+path+"?"+SignatureParam+"=") {
		t.Fatalf("SignedURL = %q, want the proof appended as a query parameter", url)
	}

	withQuery := signer.SignedURL(base+path+"?a=b", path, "user-123", now.Add(time.Hour))
	if !strings.Contains(withQuery, "?a=b&"+SignatureParam+"=") {
		t.Errorf("SignedURL = %q, want the proof appended to the existing query", withQuery)
	}
}

// fixedAuthenticator stands in for the wrapped authenticator.
type fixedAuthenticator struct {
	subject    string
	err        error
	called     bool
	challenge_ bool
}

func (f *fixedAuthenticator) Authenticate(context.Context, *http.Request) (string, error) {
	f.called = true
	return f.subject, f.err
}

func (f *fixedAuthenticator) challenge(w http.ResponseWriter) {
	f.challenge_ = true
	w.WriteHeader(http.StatusUnauthorized)
}

func (f *fixedAuthenticator) metadata() protectedResourceMetadata {
	return protectedResourceMetadata{Resource: "https://tine.cainy.dev"}
}

func TestWithSignedURLs(t *testing.T) {
	t.Parallel()

	const (
		path    = "/john/shopware/edc1e8b0"
		subject = "user-123"
	)

	now := time.Unix(1_700_000_000, 0)
	signer := testSigner(t, testMasterKey)

	t.Run("valid signature does not reach the wrapped authenticator", func(t *testing.T) {
		t.Parallel()

		next := &fixedAuthenticator{err: ErrUnauthenticated}
		auth := &signedURLAuthenticator{signer: signer, next: next, now: func() time.Time { return now }}

		token := signer.Sign(path, subject, now.Add(time.Hour))
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path+"?"+SignatureParam+"="+token, nil)

		got, err := auth.Authenticate(t.Context(), r)
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if got != subject {
			t.Errorf("subject = %q, want %q", got, subject)
		}
		if next.called {
			t.Error("wrapped authenticator was called despite a valid signature")
		}
	})

	t.Run("no signature falls through", func(t *testing.T) {
		t.Parallel()

		next := &fixedAuthenticator{subject: "from-oidc"}
		auth := &signedURLAuthenticator{signer: signer, next: next, now: func() time.Time { return now }}

		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil)

		got, err := auth.Authenticate(t.Context(), r)
		if err != nil {
			t.Fatalf("Authenticate: %v", err)
		}
		if got != "from-oidc" {
			t.Errorf("subject = %q, want the wrapped authenticator's subject", got)
		}
		if !next.called {
			t.Error("wrapped authenticator was not called")
		}
	})

	t.Run("bad signature is rejected without falling through", func(t *testing.T) {
		t.Parallel()

		next := &fixedAuthenticator{subject: "from-oidc"}
		auth := &signedURLAuthenticator{signer: signer, next: next, now: func() time.Time { return now }}

		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path+"?"+SignatureParam+"=forged.signature", nil)

		if _, err := auth.Authenticate(t.Context(), r); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("error = %v, want ErrUnauthenticated", err)
		}
		if next.called {
			t.Error("a forged signature fell through to the wrapped authenticator")
		}
	})

	t.Run("expired signature does not fall through", func(t *testing.T) {
		t.Parallel()

		next := &fixedAuthenticator{subject: "from-oidc"}
		auth := &signedURLAuthenticator{signer: signer, next: next, now: func() time.Time { return now.Add(2 * time.Hour) }}

		token := signer.Sign(path, subject, now.Add(time.Hour))
		r := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path+"?"+SignatureParam+"="+token, nil)

		if _, err := auth.Authenticate(t.Context(), r); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("error = %v, want ErrUnauthenticated", err)
		}
		if next.called {
			t.Error("an expired signature fell through to the wrapped authenticator")
		}
	})

	t.Run("challenge and metadata delegate", func(t *testing.T) {
		t.Parallel()

		next := &fixedAuthenticator{}
		auth := &signedURLAuthenticator{signer: signer, next: next, now: func() time.Time { return now }}

		auth.challenge(httptest.NewRecorder())
		if !next.challenge_ {
			t.Error("challenge did not delegate")
		}
		if auth.metadata().Resource != "https://tine.cainy.dev" {
			t.Error("metadata did not delegate")
		}
	})
}

// tamper flips a character in a token's payload, leaving it well-formed.
func tamper(token string) string {
	payload, mac, _ := strings.Cut(token, ".")

	flipped := []byte(payload)
	if flipped[0] == 'A' {
		flipped[0] = 'B'
	} else {
		flipped[0] = 'A'
	}
	return string(flipped) + "." + mac
}
