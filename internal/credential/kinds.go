package credential

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// ErrNotRefreshable is returned by Refresh for credentials with nothing to
// refresh. Callers stop on it rather than retrying.
var ErrNotRefreshable = errors.New("credential cannot be refreshed")

// None authenticates nothing. Integrations against public APIs use it.
type None struct{}

// Kind identifies this credential as requiring no authentication.
func (None) Kind() Kind { return KindNone }

// Apply leaves the request unchanged.
func (None) Apply(context.Context, *http.Request) error { return nil }

// Refresh always fails: there is nothing to refresh.
func (None) Refresh(context.Context) error { return ErrNotRefreshable }

// Bearer sends a static token in the Authorization header.
type Bearer struct {
	Token string `json:"token"`
}

// Kind identifies this credential as a static bearer token.
func (Bearer) Kind() Kind { return KindBearer }

// Apply sets the Authorization header.
func (b Bearer) Apply(_ context.Context, req *http.Request) error {
	if b.Token == "" {
		return errors.New("bearer credential has an empty token")
	}
	req.Header.Set("Authorization", "Bearer "+b.Token)
	return nil
}

// Refresh always fails: a static token cannot be renewed.
func (Bearer) Refresh(context.Context) error { return ErrNotRefreshable }

// Header sends a static value in an arbitrary header, covering the many APIs
// that use X-Api-Key and friends rather than Authorization.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Kind identifies this credential as a static header value.
func (Header) Kind() Kind { return KindHeader }

// Apply sets the configured header.
func (h Header) Apply(_ context.Context, req *http.Request) error {
	if h.Name == "" {
		return errors.New("header credential has an empty name")
	}
	req.Header.Set(h.Name, h.Value)
	return nil
}

// Refresh always fails: a static header cannot be renewed.
func (Header) Refresh(context.Context) error { return ErrNotRefreshable }

// Basic sends HTTP basic authentication.
type Basic struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Kind identifies this credential as HTTP basic authentication.
func (Basic) Kind() Kind { return KindBasic }

// Apply sets the basic Authorization header.
func (b Basic) Apply(_ context.Context, req *http.Request) error {
	req.SetBasicAuth(b.Username, b.Password)
	return nil
}

// Refresh always fails: static credentials cannot be renewed.
func (Basic) Refresh(context.Context) error { return ErrNotRefreshable }

// Marshal encodes a credential's secret payload for sealing.
func Marshal(c Credential) ([]byte, error) {
	data, err := json.Marshal(c)
	if err != nil {
		return nil, fmt.Errorf("marshal %s credential: %w", c.Kind(), err)
	}
	return data, nil
}

// Unmarshal decodes a sealed payload back into a credential of the given kind.
func Unmarshal(kind Kind, data []byte) (Credential, error) {
	switch kind {
	case KindNone:
		return None{}, nil
	case KindBearer:
		return decode[Bearer](data, kind)
	case KindHeader:
		return decode[Header](data, kind)
	case KindBasic:
		return decode[Basic](data, kind)
	case KindOAuth2:
		return nil, fmt.Errorf("credential kind %q is not implemented yet", kind)
	default:
		return nil, fmt.Errorf("unknown credential kind %q", kind)
	}
}

func decode[T Credential](data []byte, kind Kind) (Credential, error) {
	var out T
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode %s credential: %w", kind, err)
	}
	return out, nil
}
