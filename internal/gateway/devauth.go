package gateway

import (
	"context"
	"net/http"
)

// devAuthenticator accepts every request and reports a fixed subject.
//
// It exists so the gateway can be exercised without an identity provider. It is
// only reachable when TINE_DEV_SUBJECT is set, which config rejects unless
// TINE_DEV_MODE is also on.
type devAuthenticator struct {
	subject     string
	resourceURL string
}

// NewDevAuthenticator returns an Authenticator that treats every caller as
// subject, bypassing token validation entirely.
func NewDevAuthenticator(subject, resourceURL string) Authenticator {
	return &devAuthenticator{subject: subject, resourceURL: resourceURL}
}

func (d *devAuthenticator) Authenticate(context.Context, *http.Request) (string, error) {
	return d.subject, nil
}

func (d *devAuthenticator) challenge(w http.ResponseWriter) {
	// Unreachable while Authenticate always succeeds, but the interface needs it.
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}

func (d *devAuthenticator) metadata() protectedResourceMetadata {
	return protectedResourceMetadata{
		Resource:      d.resourceURL,
		BearerMethods: []string{"header"},
	}
}
