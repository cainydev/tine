package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cainydev/tine/integrations"
	"github.com/cainydev/tine/internal/credential"
)

// CredentialLoader returns the credential bound to one instance.
type CredentialLoader interface {
	LoadCredential(ctx context.Context, instanceID string) (credential.Credential, error)
}

// ParamLoader returns an instance's configured settings.
type ParamLoader interface {
	LoadParams(ctx context.Context, instanceID string) (map[string]string, error)
}

// IntegrationBuilder builds a per-instance MCP server from the registry.
//
// The server exposes exactly one integration's tools, bound to exactly one
// credential. Nothing is shared between instances, which is what makes a leak
// across tenants structurally impossible rather than merely avoided.
type IntegrationBuilder struct {
	registry *integrations.Registry
	creds    CredentialLoader
	params   ParamLoader
	client   *http.Client
}

// NewIntegrationBuilder returns a Builder over the given registry.
func NewIntegrationBuilder(
	r *integrations.Registry,
	creds CredentialLoader,
	params ParamLoader,
	client *http.Client,
) *IntegrationBuilder {
	return &IntegrationBuilder{registry: r, creds: creds, params: params, client: client}
}

// Build implements Builder.
func (b *IntegrationBuilder) Build(ctx context.Context, inst *Instance) (*mcp.Server, error) {
	in, ok := b.registry.Get(inst.IntegrationSlug)
	if !ok {
		return nil, fmt.Errorf("integration %q is not registered", inst.IntegrationSlug)
	}

	if got := in.Version(); got != inst.Version {
		return nil, fmt.Errorf("integration %q is version %s, instance expects %s",
			inst.IntegrationSlug, got, inst.Version)
	}

	rawParams, err := b.params.LoadParams(ctx, inst.ID)
	if err != nil {
		return nil, fmt.Errorf("load params: %w", err)
	}
	params, err := integrations.ValidateParams(in, rawParams)
	if err != nil {
		return nil, fmt.Errorf("instance %s: %w", inst.ID, err)
	}

	cred, err := b.creds.LoadCredential(ctx, inst.ID)
	if err != nil {
		return nil, fmt.Errorf("load credential: %w", err)
	}

	tools, err := in.Bind(ctx, &integrations.Binding{
		InstanceID: inst.ID,
		Params:     params,
		Credential: cred,
		HTTP:       b.client,
	})
	if err != nil {
		return nil, fmt.Errorf("bind integration %q: %w", inst.IntegrationSlug, err)
	}

	srv := mcp.NewServer(&mcp.Implementation{
		Name:    in.Slug(),
		Version: in.Version(),
		Title:   inst.DisplayName,
	}, nil)

	for _, tool := range tools {
		tool.Register(srv)
	}
	return srv, nil
}

// DecodeParams unmarshals an instance's stored JSON parameters.
func DecodeParams(raw string) (map[string]string, error) {
	if raw == "" {
		return map[string]string{}, nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode params: %w", err)
	}
	return out, nil
}
