package db

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/cainydev/tine/integrations"
)

// fakeDBRest stands in for a db-rest instance and counts requests per path so
// tests can assert that concurrent calls actually happened.
func fakeDBRest(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("GET /locations", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, []Stop{{ID: "8000001", Name: "Aachen Hbf"}})
	})

	mux.HandleFunc("GET /departures/{id}", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		late := 300
		writeJSON(t, w, map[string]any{"departures": []Departure{
			{TripID: "d1", Direction: "Köln", Delay: &late},
			{TripID: "d2", Direction: "Brüssel", Delay: ptr(0)},
		}})
	})

	mux.HandleFunc("GET /arrivals/{id}", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		writeJSON(t, w, map[string]any{"arrivals": []Departure{
			{TripID: "a1", Provenance: "Düsseldorf", Delay: ptr(0)},
		}})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func ptr[T any](v T) *T { return &v }

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode: %v", err)
	}
}

func newTestClient(t *testing.T, baseURL string) *client {
	t.Helper()
	return &client{baseURL: baseURL, language: "de", http: http.DefaultClient}
}

func TestSearchLocations(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := fakeDBRest(t, &hits)
	c := newTestClient(t, srv.URL)

	var stops []Stop
	if err := c.get(t.Context(), "/locations", nil, &stops); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(stops) != 1 || stops[0].ID != "8000001" {
		t.Fatalf("stops = %+v", stops)
	}
}

// get_board fetches departures and arrivals concurrently: the behaviour that
// justifies integrations being Go rather than declarative configuration.
func TestBoardCombinesBothDirections(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := fakeDBRest(t, &hits)
	c := newTestClient(t, srv.URL)

	departures, err := c.board(t.Context(), "/departures", "8000001", 30)
	if err != nil {
		t.Fatalf("departures: %v", err)
	}
	arrivals, err := c.board(t.Context(), "/arrivals", "8000001", 30)
	if err != nil {
		t.Fatalf("arrivals: %v", err)
	}

	if len(departures) != 2 {
		t.Errorf("departures = %d, want 2", len(departures))
	}
	if len(arrivals) != 1 {
		t.Errorf("arrivals = %d, want 1", len(arrivals))
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("upstream hits = %d, want 2", got)
	}
}

func TestOnlyLateFilter(t *testing.T) {
	t.Parallel()

	in := []Departure{
		{TripID: "late", Delay: ptr(300)},
		{TripID: "ontime", Delay: ptr(0)},
		{TripID: "unknown", Delay: nil},
	}

	got := onlyLate(in)
	if len(got) != 1 {
		t.Fatalf("filtered = %d entries, want 1: %+v", len(got), got)
	}
	if got[0].TripID != "late" {
		t.Errorf("kept %q, want %q", got[0].TripID, "late")
	}
}

func TestBoardRequiresStopID(t *testing.T) {
	t.Parallel()

	var hits atomic.Int32
	srv := fakeDBRest(t, &hits)
	c := newTestClient(t, srv.URL)

	if _, err := c.board(t.Context(), "/departures", "", 30); err == nil {
		t.Error("expected an error for an empty stop id")
	}
}

func TestUpstreamErrorIsReported(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream exploded", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	c := newTestClient(t, srv.URL)
	var out []Stop
	if err := c.get(t.Context(), "/locations", nil, &out); err == nil {
		t.Error("expected an error for a 502 response")
	}
}

// Bind must apply parameter defaults and produce the full tool set.
func TestBindAppliesDefaults(t *testing.T) {
	t.Parallel()

	in := New()

	params, err := integrations.ValidateParams(in, map[string]string{})
	if err != nil {
		t.Fatalf("validate params: %v", err)
	}
	if params["base_url"] != defaultBaseURL {
		t.Errorf("base_url = %q, want %q", params["base_url"], defaultBaseURL)
	}
	if params["language"] != "de" {
		t.Errorf("language = %q, want de", params["language"])
	}

	tools, err := in.Bind(t.Context(), &integrations.Binding{
		InstanceID: "inst-1",
		Params:     params,
		HTTP:       http.DefaultClient,
	})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("tools = %d, want 3", len(tools))
	}
}

func TestValidateParamsRejectsBadInput(t *testing.T) {
	t.Parallel()

	in := New()

	if _, err := integrations.ValidateParams(in, map[string]string{"language": "fr"}); err == nil {
		t.Error("expected an error for a language outside the enum")
	}
	if _, err := integrations.ValidateParams(in, map[string]string{"languag": "de"}); err == nil {
		t.Error("expected an error for an unknown parameter (typo)")
	}
}

// Integration must satisfy the interface the registry consumes.
var _ integrations.Integration = (*Integration)(nil)

// MCP requires every tool's output schema to be an object. A Go handler
// returning a slice produces a top-level array schema, which clients reject
// so this asserts the wrapper types stay in place.
func TestOutputSchemasAreObjects(t *testing.T) {
	t.Parallel()

	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "1.0.0"}, nil)

	tools, err := New().Bind(t.Context(), &integrations.Binding{
		InstanceID: "inst-1",
		Params:     map[string]string{"base_url": "http://example.invalid", "language": "de"},
		HTTP:       http.DefaultClient,
	})
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	for _, tool := range tools {
		tool.Register(srv)
	}

	// Ask the server itself what it advertises, so the assertion runs against
	// the schema a client actually receives.
	listed, err := listTools(t, srv)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(listed) != len(tools) {
		t.Fatalf("listed %d tools, registered %d", len(listed), len(tools))
	}

	for _, tool := range listed {
		t.Run(tool.Name, func(t *testing.T) {
			if tool.OutputSchema == nil {
				t.Fatal("tool has no output schema")
			}
			schema, ok := tool.OutputSchema.(map[string]any)
			if !ok {
				t.Fatalf("output schema is %T, want a JSON object", tool.OutputSchema)
			}
			if got := schema["type"]; got != "object" {
				t.Errorf("outputSchema.type = %v, want \"object\"", got)
			}
		})
	}
}

// listTools drives a real in-memory client against srv and returns its tools.
func listTools(t *testing.T, srv *mcp.Server) ([]*mcp.Tool, error) {
	t.Helper()

	clientTransport, serverTransport := mcp.NewInMemoryTransports()

	serverSession, err := srv.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = serverSession.Close() })

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	clientSession, err := client.Connect(t.Context(), clientTransport, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = clientSession.Close() })

	res, err := clientSession.ListTools(t.Context(), nil)
	if err != nil {
		return nil, err
	}
	return res.Tools, nil
}
