package gateway

import (
	"context"
	"net/http"
	"strings"
)

// TokenVerifier resolves a bearer token to the subject it authenticates as.
//
// instanceID is the instance the request addresses, so a token scoped to one
// endpoint can be rejected at another. Implemented by the store; defined here
// because the gateway is the consumer.
type TokenVerifier interface {
	VerifyToken(ctx context.Context, token, instanceID string) (subject string, err error)
}

// TokenPrefix distinguishes a tine-issued token from an identity provider's
// JWT in the same header.
const TokenPrefix = "tine_"

// tokenAuthenticator accepts tine-issued bearer tokens and otherwise falls back
// to the authenticator it wraps.
type tokenAuthenticator struct {
	verifier TokenVerifier
	next     Authenticator
}

// WithBearerTokens returns an Authenticator that additionally accepts tokens
// issued by tine itself.
//
// Only values carrying the tine prefix are claimed, so a JWT in the same header
// still reaches the wrapped authenticator untouched.
func WithBearerTokens(next Authenticator, v TokenVerifier) Authenticator {
	return &tokenAuthenticator{verifier: v, next: next}
}

func (a *tokenAuthenticator) Authenticate(ctx context.Context, r *http.Request) (string, error) {
	raw, ok := bearerToken(r)
	if !ok || !strings.HasPrefix(raw, TokenPrefix) {
		return a.next.Authenticate(ctx, r)
	}

	subject, err := a.verifier.VerifyToken(ctx, raw, r.PathValue("id"))
	if err != nil {
		return "", err
	}
	return subject, nil
}

func (a *tokenAuthenticator) challenge(w http.ResponseWriter) { a.next.challenge(w) }

func (a *tokenAuthenticator) metadata() protectedResourceMetadata { return a.next.metadata() }
