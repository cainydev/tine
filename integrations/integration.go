// Package integrations defines what an integration is and holds the registry of
// those tine ships with.
//
// Integrations are compiled in: they arrive by pull request, are reviewed, and
// become part of the binary. There is therefore no sandbox and no configuration
// language, an integration is ordinary Go code, free to make several calls,
// combine results, or speak a protocol that is not HTTP at all.
package integrations

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cainydev/tine/internal/credential"
)

// Integration describes one API that tine can expose as an MCP endpoint.
type Integration interface {
	Slug() string

	Name() string

	Version() string

	Params() []ParamSpec

	Credentials() []credential.Kind

	Bind(ctx context.Context, b *Binding) ([]Tool, error)
}

// Binding is everything an integration needs to serve one instance.
type Binding struct {
	InstanceID string

	Params map[string]string

	Credential credential.Credential

	HTTP *http.Client
}

// Tool is one callable exposed by an integration.
type Tool struct {
	Name     string
	Register func(*mcp.Server)
}

// ParamSpec describes one instance-level setting.
type ParamSpec struct {
	Key         string
	Description string
	Required    bool
	Default     string

	Enum []string
}

// Registry holds the integrations compiled into this binary.
type Registry struct {
	mu    sync.RWMutex
	items map[string]Integration
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{items: make(map[string]Integration)}
}

// Register adds an integration.
func (r *Registry) Register(in Integration) error {
	slug := in.Slug()
	if slug == "" {
		return fmt.Errorf("integration %T has an empty slug", in)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.items[slug]; ok {
		return fmt.Errorf("slug %q is already registered by %T", slug, existing)
	}
	r.items[slug] = in
	return nil
}

// Get returns the integration with the given slug.
func (r *Registry) Get(slug string) (Integration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	in, ok := r.items[slug]
	return in, ok
}

// All returns every registered integration, ordered by slug so listings are
// stable across runs.
func (r *Registry) All() []Integration {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := slices.Collect(maps.Values(r.items))
	sort.Slice(out, func(i, j int) bool { return out[i].Slug() < out[j].Slug() })
	return out
}

// AcceptsCredential reports whether an integration can use a credential kind.
func AcceptsCredential(in Integration, kind credential.Kind) bool {
	return slices.Contains(in.Credentials(), kind)
}

// ValidateParams checks an instance's settings against an integration's specs
// and returns them with defaults applied.
func ValidateParams(in Integration, params map[string]string) (map[string]string, error) {
	specs := in.Params()
	known := make(map[string]ParamSpec, len(specs))
	for _, s := range specs {
		known[s.Key] = s
	}

	for key := range params {
		if _, ok := known[key]; !ok {
			return nil, fmt.Errorf("unknown parameter %q for integration %q", key, in.Slug())
		}
	}

	out := make(map[string]string, len(specs))
	for _, spec := range specs {
		value, present := params[spec.Key]
		if !present || value == "" {
			if spec.Required {
				return nil, fmt.Errorf("parameter %q is required for integration %q", spec.Key, in.Slug())
			}
			value = spec.Default
		}
		if len(spec.Enum) > 0 && value != "" && !slices.Contains(spec.Enum, value) {
			return nil, fmt.Errorf("parameter %q must be one of %v, got %q", spec.Key, spec.Enum, value)
		}
		out[spec.Key] = value
	}
	return out, nil
}
