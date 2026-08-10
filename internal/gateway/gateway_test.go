package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeResolver serves instances from a map keyed by "user/integration/id".
type fakeResolver struct {
	instances map[string]*Instance
	err       error
}

func (f *fakeResolver) Resolve(_ context.Context, user, integration, id string) (*Instance, error) {
	if f.err != nil {
		return nil, f.err
	}
	inst, ok := f.instances[user+"/"+integration+"/"+id]
	if !ok {
		return nil, ErrNotFound
	}
	return inst, nil
}

// fakeBuilder records which instances it was asked to build.
type fakeBuilder struct {
	built []string
	err   error
}

func (f *fakeBuilder) Build(_ context.Context, inst *Instance) (*mcp.Server, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.built = append(f.built, inst.ID)
	return mcp.NewServer(&mcp.Implementation{Name: inst.IntegrationSlug, Version: inst.Version}, nil), nil
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// initializeBody is a minimal MCP request, enough to reach the server builder.
const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize",` +
	`"params":{"protocolVersion":"2025-06-18","capabilities":{},` +
	`"clientInfo":{"name":"test","version":"1"}}}`

func TestRouting(t *testing.T) {
	t.Parallel()

	ti := newTestIssuer(t)
	const audience = "tine"
	auth := newTestAuthenticator(t, ti, audience)

	ownerToken := ti.token(t, "owner-sub", audience, time.Now().Add(time.Hour))
	strangerToken := ti.token(t, "someone-else", audience, time.Now().Add(time.Hour))

	resolver := &fakeResolver{instances: map[string]*Instance{
		"john/shopware/inst-1": {
			ID: "inst-1", UserSlug: "john", IntegrationSlug: "shopware",
			OwnerSubject: "owner-sub", Version: "1.0.0",
		},
	}}

	tests := []struct {
		name       string
		method     string
		path       string
		token      string
		wantStatus int
	}{
		{"owner", http.MethodPost, "/john/shopware/inst-1", ownerToken, http.StatusOK},
		{"no token", http.MethodPost, "/john/shopware/inst-1", "", http.StatusUnauthorized},
		{"garbage token", http.MethodPost, "/john/shopware/inst-1", "not-a-jwt", http.StatusUnauthorized},
		{
			"expired token", http.MethodPost, "/john/shopware/inst-1",
			ti.token(t, "owner-sub", audience, time.Now().Add(-time.Hour)), http.StatusUnauthorized,
		},
		// A valid token for a different subject must not reveal that the
		// instance exists, so this is 404 rather than 403.
		{"valid token, wrong owner", http.MethodPost, "/john/shopware/inst-1", strangerToken, http.StatusNotFound},
		{"unknown id", http.MethodPost, "/john/shopware/nope", ownerToken, http.StatusNotFound},
		{"right id, wrong integration", http.MethodPost, "/john/billbee/inst-1", ownerToken, http.StatusNotFound},
		{"right id, wrong user", http.MethodPost, "/nobody/shopware/inst-1", ownerToken, http.StatusNotFound},
		// Stateless mode routes POST only.
		{"GET not routed", http.MethodGet, "/john/shopware/inst-1", ownerToken, http.StatusMethodNotAllowed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			gw := New(resolver, &fakeBuilder{}, auth, discardLogger())
			req := httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, strings.NewReader(initializeBody))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}

			rec := httptest.NewRecorder()
			gw.Handler().ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body.String())
			}
		})
	}
}

// Each request must build a server for its own instance only. This is the
// core isolation guarantee: no instance ever sees another's tools.
func TestInstanceIsolation(t *testing.T) {
	t.Parallel()

	ti := newTestIssuer(t)
	const audience = "tine"
	auth := newTestAuthenticator(t, ti, audience)
	tok := ti.token(t, "owner-sub", audience, time.Now().Add(time.Hour))

	resolver := &fakeResolver{instances: map[string]*Instance{
		"john/shopware/a": {ID: "inst-a", IntegrationSlug: "shopware", OwnerSubject: "owner-sub", Version: "1.0.0"},
		"john/billbee/b":  {ID: "inst-b", IntegrationSlug: "billbee", OwnerSubject: "owner-sub", Version: "1.0.0"},
	}}
	builder := &fakeBuilder{}
	gw := New(resolver, builder, auth, discardLogger())

	for _, path := range []string{"/john/shopware/a", "/john/billbee/b", "/john/shopware/a"} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, strings.NewReader(initializeBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Authorization", "Bearer "+tok)
		gw.Handler().ServeHTTP(httptest.NewRecorder(), req)
	}

	want := []string{"inst-a", "inst-b", "inst-a"}
	if len(builder.built) != len(want) {
		t.Fatalf("built %v, want %v", builder.built, want)
	}
	for i, id := range want {
		if builder.built[i] != id {
			t.Errorf("build %d = %q, want %q", i, builder.built[i], id)
		}
	}
}

func TestResolverFailureIsInternalError(t *testing.T) {
	t.Parallel()

	ti := newTestIssuer(t)
	const audience = "tine"
	auth := newTestAuthenticator(t, ti, audience)

	resolver := &fakeResolver{err: errors.New("database is down")}
	gw := New(resolver, &fakeBuilder{}, auth, discardLogger())

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/john/shopware/x", strings.NewReader(initializeBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+ti.token(t, "owner-sub", audience, time.Now().Add(time.Hour)))
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	// A resolver failure must not leak its cause to the caller.
	if strings.Contains(rec.Body.String(), "database") {
		t.Errorf("response leaked internal error: %s", rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	t.Parallel()

	ti := newTestIssuer(t)
	gw := New(&fakeResolver{}, &fakeBuilder{}, newTestAuthenticator(t, ti, "tine"), discardLogger())
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	gw.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusOK)
	}
}
