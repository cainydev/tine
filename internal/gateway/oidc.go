package gateway

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/coreos/go-oidc/v3/oidc"
)

// ErrUnauthenticated is returned when a request carries no usable token.
var ErrUnauthenticated = errors.New("unauthenticated")

// Authenticator verifies the bearer token presented by an MCP client.
//
// tine acts as an OAuth 2.1 resource server: it validates tokens but never
// issues them. Any OIDC-compliant issuer works, WorkOS, Authentik, Keycloak,
// Zitadel, because the only coupling is the issuer URL.
type Authenticator interface {
	Authenticate(ctx context.Context, r *http.Request) (subject string, err error)

	challenge(w http.ResponseWriter)

	metadata() protectedResourceMetadata
}

// OIDCAuthenticator validates JWTs against an OIDC provider's JWKS.
type OIDCAuthenticator struct {
	verifier *oidc.IDTokenVerifier

	resourceURL string

	authzServers []string
}

// NewOIDCAuthenticator discovers the issuer's configuration and returns an
// authenticator that verifies tokens against it.
func NewOIDCAuthenticator(ctx context.Context, issuer, audience, resourceURL string) (*OIDCAuthenticator, error) {
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("discover oidc issuer %q: %w", issuer, err)
	}

	return &OIDCAuthenticator{
		verifier:     provider.Verifier(&oidc.Config{ClientID: audience}),
		resourceURL:  resourceURL,
		authzServers: []string{issuer},
	}, nil
}

// Authenticate verifies the request's bearer token and returns its subject.
func (a *OIDCAuthenticator) Authenticate(ctx context.Context, r *http.Request) (string, error) {
	raw, ok := bearerToken(r)
	if !ok {
		return "", ErrUnauthenticated
	}

	tok, err := a.verifier.Verify(ctx, raw)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnauthenticated, err)
	}
	if tok.Subject == "" {
		return "", fmt.Errorf("%w: token has no subject", ErrUnauthenticated)
	}
	return tok.Subject, nil
}

// challenge writes the 401 that starts an MCP client's OAuth flow.
func (a *OIDCAuthenticator) challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(
		`Bearer realm="tine", resource_metadata=%q`,
		a.resourceURL+protectedResourcePath,
	))
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

// protectedResourcePath is the well-known location defined by RFC 9728.
const protectedResourcePath = "/.well-known/oauth-protected-resource"

// protectedResourceMetadata is the RFC 9728 document describing this server.
type protectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	BearerMethods        []string `json:"bearer_methods_supported"`
}

func (a *OIDCAuthenticator) metadata() protectedResourceMetadata {
	return protectedResourceMetadata{
		Resource:             a.resourceURL,
		AuthorizationServers: a.authzServers,
		BearerMethods:        []string{"header"},
	}
}
