// Package credential holds the auth material for a single integration instance
// and applies it to outbound requests.
package credential

import (
	"context"
	"net/http"
)

// Kind identifies how a credential authenticates against an upstream API.
type Kind string

// Kinds of credential tine can apply to an upstream request.
const (
	KindNone   Kind = "none"
	KindBearer Kind = "bearer"
	KindHeader Kind = "header"
	KindBasic  Kind = "basic"
	KindOAuth2 Kind = "oauth2"
)

// Credential applies auth to an outbound request. Implementations are scoped to
// exactly one integration instance, so a given Credential only ever holds the
// secrets of a single tenant.
type Credential interface {
	Kind() Kind

	Apply(ctx context.Context, req *http.Request) error

	Refresh(ctx context.Context) error
}
