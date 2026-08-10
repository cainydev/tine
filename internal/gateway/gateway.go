// Package gateway routes HTTP requests to per-instance MCP servers.
//
// Each configured integration instance is reachable at
// /<user>/<integration>/<id> and gets its own MCP server exposing only that
// integration's tools, authenticated with only that instance's credential.
// Nothing is merged across instances.
//
// Callers authenticate with an OAuth 2.1 bearer token issued by an external
// OIDC provider; tine validates but never issues tokens.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrNotFound is returned by a Resolver when no enabled instance matches.
var ErrNotFound = errors.New("instance not found")

// Instance is a resolved integration instance: the unit that owns one MCP
// endpoint.
type Instance struct {
	// ID is the stable public identifier in the URL's third segment. It is
	// minted once at creation and never derived from configuration, so editing
	// an instance never changes its endpoint.
	ID string

	// OwnerSubject is the OIDC subject permitted to use this endpoint.
	OwnerSubject string

	UserSlug        string
	IntegrationSlug string
	DisplayName     string
	Version         string
}

// Resolver looks up the instance addressed by a request path. Implemented by
// the store; defined here because the gateway is the consumer.
//
// All three path segments are passed: the id alone identifies the instance, but
// the user and integration segments must match the stored values, so a guessed
// id under the wrong path does not resolve.
type Resolver interface {
	Resolve(ctx context.Context, userSlug, integrationSlug, id string) (*Instance, error)
}

// Builder turns a resolved instance into an MCP server exposing that
// integration's tools, bound to that instance's credential.
type Builder interface {
	Build(ctx context.Context, inst *Instance) (*mcp.Server, error)
}

// Gateway serves every instance endpoint on one HTTP listener.
type Gateway struct {
	resolver Resolver
	builder  Builder
	auth     Authenticator
	log      *slog.Logger
}

// New returns a Gateway that resolves instances with r, builds servers with b,
// and authenticates callers with auth.
func New(r Resolver, b Builder, auth Authenticator, log *slog.Logger) *Gateway {
	return &Gateway{resolver: r, builder: b, auth: auth, log: log}
}

// Handler returns the root HTTP handler.
//
// Routing uses net/http's method+wildcard patterns, so /<user>/<instance> is
// matched without a third-party router.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()

	// The MCP endpoint itself. Stateless mode rejects GET and DELETE with 405,
	// so only POST is routed here.
	mcpHandler := mcp.NewStreamableHTTPHandler(g.serverForRequest, &mcp.StreamableHTTPOptions{
		Stateless: true,
	})
	mux.Handle("POST /{user}/{integration}/{id}", g.withInstance(mcpHandler))

	// RFC 9728. Clients fetch this after a 401 to discover the authorization
	// server, so it must be reachable without a token.
	mux.HandleFunc("GET "+protectedResourcePath, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(g.auth.metadata()); err != nil {
			g.log.ErrorContext(r.Context(), "encode resource metadata", slog.Any("error", err))
		}
	})

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte("ok")); err != nil {
			g.log.DebugContext(r.Context(), "healthz write", slog.Any("error", err))
		}
	})

	return mux
}

// contextKey is unexported so no other package can collide with our keys.
type contextKey struct{ name string }

var instanceKey = &contextKey{"instance"}

// withInstance resolves the instance named in the path, authenticates the
// request against it, and stores it on the context for serverForRequest.
//
// Resolution happens here rather than in serverForRequest because that callback
// cannot report an error: returning nil yields a bare 400, losing the
// distinction between "no such instance" and "wrong token".
func (g *Gateway) withInstance(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		// Authenticate before resolving: an anonymous caller must not be able to
		// probe which instances exist by comparing 401 against 404.
		subject, err := g.auth.Authenticate(ctx, r)
		if err != nil {
			g.log.DebugContext(ctx, "authentication failed", slog.Any("error", err))
			g.auth.challenge(w)
			return
		}

		userSlug := r.PathValue("user")
		integrationSlug := r.PathValue("integration")
		id := r.PathValue("id")

		inst, err := g.resolver.Resolve(ctx, userSlug, integrationSlug, id)
		switch {
		case errors.Is(err, ErrNotFound):
			http.Error(w, "not found", http.StatusNotFound)
			return
		case err != nil:
			g.log.ErrorContext(ctx, "resolve instance",
				slog.String("user", userSlug),
				slog.String("integration", integrationSlug),
				slog.String("id", id),
				slog.Any("error", err))
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		// Authenticated as someone, but not the owner. 404 rather than 403: a
		// valid token for another account learns nothing about this one.
		if inst.OwnerSubject != subject {
			g.log.WarnContext(ctx, "subject mismatch on instance",
				slog.String("instance", inst.ID),
				slog.String("subject", subject))
			http.Error(w, "not found", http.StatusNotFound)
			return
		}

		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, instanceKey, inst)))
	})
}

// serverForRequest builds the MCP server for the instance resolved by
// withInstance. Returning nil makes the SDK serve 400 Bad Request.
func (g *Gateway) serverForRequest(r *http.Request) *mcp.Server {
	ctx := r.Context()

	inst, ok := ctx.Value(instanceKey).(*Instance)
	if !ok {
		g.log.ErrorContext(ctx, "no instance on context; routing is misconfigured")
		return nil
	}

	srv, err := g.builder.Build(ctx, inst)
	if err != nil {
		g.log.ErrorContext(ctx, "build mcp server",
			slog.String("instance", inst.ID),
			slog.Any("error", err))
		return nil
	}
	return srv
}
