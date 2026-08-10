package gateway

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// testIssuer is a minimal OIDC provider: a discovery document, a JWKS, and a
// signing key. Tokens are signed for real and verified for real, so these tests
// exercise the actual verification path rather than a stubbed one.
type testIssuer struct {
	server *httptest.Server
	key    *rsa.PrivateKey
	kid    string
}

func newTestIssuer(t *testing.T) *testIssuer {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	ti := &testIssuer{key: key, kid: "test-key-1"}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, map[string]any{
			"issuer":                                ti.server.URL,
			"jwks_uri":                              ti.server.URL + "/jwks",
			"authorization_endpoint":                ti.server.URL + "/authorize",
			"token_endpoint":                        ti.server.URL + "/token",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})

	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		writeJSON(t, w, map[string]any{"keys": []map[string]any{{
			"kty": "RSA",
			"kid": ti.kid,
			"alg": "RS256",
			"use": "sig",
			"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})

	ti.server = httptest.NewServer(mux)
	t.Cleanup(ti.server.Close)
	return ti
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode test response: %v", err)
	}
}

// token mints a signed JWT with the given subject, audience and expiry.
func (ti *testIssuer) token(t *testing.T, subject, audience string, expiry time.Time) string {
	t.Helper()

	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": ti.server.URL,
		"sub": subject,
		"aud": audience,
		"exp": expiry.Unix(),
		"iat": time.Now().Unix(),
	})
	tok.Header["kid"] = ti.kid

	signed, err := tok.SignedString(ti.key)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

func newTestAuthenticator(t *testing.T, ti *testIssuer, audience string) *OIDCAuthenticator {
	t.Helper()

	auth, err := NewOIDCAuthenticator(t.Context(), ti.server.URL, audience, "https://tine.example")
	if err != nil {
		t.Fatalf("new authenticator: %v", err)
	}
	return auth
}

func TestAuthenticate(t *testing.T) {
	t.Parallel()

	ti := newTestIssuer(t)
	const audience = "tine"
	auth := newTestAuthenticator(t, ti, audience)

	otherIssuer := newTestIssuer(t)

	tests := []struct {
		name        string
		token       string
		wantSubject string
		wantErr     bool
	}{
		{
			name:        "valid token",
			token:       ti.token(t, "user-123", audience, time.Now().Add(time.Hour)),
			wantSubject: "user-123",
		},
		{
			name:    "expired token",
			token:   ti.token(t, "user-123", audience, time.Now().Add(-time.Hour)),
			wantErr: true,
		},
		{
			name:    "wrong audience",
			token:   ti.token(t, "user-123", "someone-else", time.Now().Add(time.Hour)),
			wantErr: true,
		},
		{
			name:    "signed by another issuer",
			token:   otherIssuer.token(t, "user-123", audience, time.Now().Add(time.Hour)),
			wantErr: true,
		},
		{
			name:    "garbage token",
			token:   "not-a-jwt",
			wantErr: true,
		},
		{
			name:    "no token",
			token:   "",
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}

			subject, err := auth.Authenticate(t.Context(), req)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got subject %q", subject)
				}
				if !errors.Is(err, ErrUnauthenticated) {
					t.Errorf("error %v does not match ErrUnauthenticated", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if subject != tc.wantSubject {
				t.Errorf("subject = %q, want %q", subject, tc.wantSubject)
			}
		})
	}
}

func TestProtectedResourceMetadata(t *testing.T) {
	t.Parallel()

	ti := newTestIssuer(t)
	auth := newTestAuthenticator(t, ti, "tine")
	gw := New(&fakeResolver{}, &fakeBuilder{}, auth, discardLogger())

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, protectedResourcePath, nil)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var got protectedResourceMetadata
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if got.Resource != "https://tine.example" {
		t.Errorf("resource = %q", got.Resource)
	}
	if len(got.AuthorizationServers) != 1 || got.AuthorizationServers[0] != ti.server.URL {
		t.Errorf("authorization_servers = %v, want [%s]", got.AuthorizationServers, ti.server.URL)
	}
	if len(got.BearerMethods) != 1 || got.BearerMethods[0] != "header" {
		t.Errorf("bearer_methods_supported = %v, want [header]", got.BearerMethods)
	}
}

// An unauthenticated request must carry the RFC 9728 pointer, or a compliant
// MCP client cannot discover where to authenticate.
func TestChallengeAdvertisesResourceMetadata(t *testing.T) {
	t.Parallel()

	ti := newTestIssuer(t)
	auth := newTestAuthenticator(t, ti, "tine")
	gw := New(&fakeResolver{}, &fakeBuilder{}, auth, discardLogger())

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/john/shopware/inst-1", nil)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}

	got := rec.Header().Get("WWW-Authenticate")
	want := `Bearer realm="tine", resource_metadata="https://tine.example` + protectedResourcePath + `"`
	if got != want {
		t.Errorf("WWW-Authenticate = %q, want %q", got, want)
	}
}

// An MCP client requests a token for a resource (RFC 8707), so the audience is
// the resource identifier rather than a client id.
func TestAcceptsResourceAsAudience(t *testing.T) {
	t.Parallel()

	ti := newTestIssuer(t)
	auth := newTestAuthenticator(t, ti, "tine")

	for _, aud := range []string{
		"https://tine.example",
		"https://tine.example/",
		"tine",
	} {
		t.Run(aud, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", nil)
			req.Header.Set("Authorization", "Bearer "+ti.token(t, "user-1", aud, time.Now().Add(time.Hour)))

			subject, err := auth.Authenticate(t.Context(), req)
			if err != nil {
				t.Fatalf("audience %q rejected: %v", aud, err)
			}
			if subject != "user-1" {
				t.Errorf("subject = %q, want user-1", subject)
			}
		})
	}
}

// A token minted for a different resource must not be accepted here.
func TestRejectsForeignAudience(t *testing.T) {
	t.Parallel()

	ti := newTestIssuer(t)
	auth := newTestAuthenticator(t, ti, "tine")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/x", nil)
	req.Header.Set("Authorization", "Bearer "+ti.token(t, "user-1", "https://other.example", time.Now().Add(time.Hour)))

	if _, err := auth.Authenticate(t.Context(), req); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("error = %v, want ErrUnauthenticated", err)
	}
}
