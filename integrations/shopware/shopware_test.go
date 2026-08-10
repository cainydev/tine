package shopware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/cainydev/tine/integrations"
	"github.com/cainydev/tine/internal/credential"
)

// fakeStore stands in for a Shopware installation: it issues tokens, counts how
// often it does, and rejects requests that do not carry the current one.
type fakeStore struct {
	server *httptest.Server

	tokenRequests atomic.Int32
	apiRequests   atomic.Int32

	mu         sync.Mutex
	current    string
	rejectNext bool
}

func newFakeStore(t *testing.T) *fakeStore {
	t.Helper()

	s := &fakeStore{current: "token-1"}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		s.tokenRequests.Add(1)

		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		if r.FormValue("grant_type") != "client_credentials" {
			http.Error(w, "unsupported grant", http.StatusBadRequest)
			return
		}
		if r.FormValue("client_id") == "" || r.FormValue("client_secret") == "" {
			http.Error(w, "missing credentials", http.StatusUnauthorized)
			return
		}

		s.mu.Lock()
		token := s.current
		s.mu.Unlock()

		writeJSON(t, w, map[string]any{
			"access_token": token,
			"token_type":   "Bearer",
			"expires_in":   600,
		})
	})

	mux.HandleFunc("POST /api/search/product", func(w http.ResponseWriter, r *http.Request) {
		s.apiRequests.Add(1)

		if !s.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(t, w, map[string]any{
			"total": 2,
			"data": []Product{
				{ID: "p1", ProductNumber: "SW-1", Name: "Tee", Active: true, Stock: 5},
				{ID: "p2", ProductNumber: "SW-2", Name: "Kanne", Active: false, Stock: 0},
			},
		})
	})

	mux.HandleFunc("POST /api/search/order", func(w http.ResponseWriter, r *http.Request) {
		s.apiRequests.Add(1)

		if !s.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(t, w, map[string]any{"total": 1, "data": []Order{{ID: "o1", OrderNumber: "10001"}}})
	})

	mux.HandleFunc("POST /api/search/customer", func(w http.ResponseWriter, r *http.Request) {
		s.apiRequests.Add(1)

		if !s.authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeJSON(t, w, map[string]any{"total": 7, "data": []any{}})
	})

	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

// authorized reports whether a request carries the token the store considers
// current, optionally rejecting once to simulate a revoked token.
func (s *fakeStore) authorized(r *http.Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.rejectNext {
		s.rejectNext = false
		s.current = "token-2"
		return false
	}
	return r.Header.Get("Authorization") == "Bearer "+s.current
}

// revokeOnce makes the next API request fail with 401.
func (s *fakeStore) revokeOnce() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejectNext = true
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode: %v", err)
	}
}

func newTestClient(t *testing.T, store *fakeStore) *client {
	t.Helper()

	cred := &credential.ClientCredentials{
		TokenURL:     TokenURL(store.server.URL),
		ClientID:     "SWIA-key",
		ClientSecret: "secret",
	}
	cred.WithClient(http.DefaultClient)

	return &client{
		baseURL:  store.server.URL,
		language: "de-DE",
		cred:     cred,
		http:     http.DefaultClient,
	}
}

// The credential must fetch a token before the first call and reuse it after.
func TestTokenIsFetchedOnceAndReused(t *testing.T) {
	t.Parallel()

	store := newFakeStore(t)
	c := newTestClient(t, store)

	for range 3 {
		var resp struct {
			Total int `json:"total"`
		}
		if err := c.post(t.Context(), "/api/search/product", map[string]any{"limit": 1}, &resp); err != nil {
			t.Fatalf("request: %v", err)
		}
	}

	if got := store.tokenRequests.Load(); got != 1 {
		t.Errorf("token requests = %d, want 1: the token should be cached", got)
	}
	if got := store.apiRequests.Load(); got != 3 {
		t.Errorf("api requests = %d, want 3", got)
	}
}

// A revoked token must be refreshed and the request retried once, without the
// caller seeing an error.
func TestRefreshesOnUnauthorized(t *testing.T) {
	t.Parallel()

	store := newFakeStore(t)
	c := newTestClient(t, store)

	var first struct {
		Total int `json:"total"`
	}
	if err := c.post(t.Context(), "/api/search/product", map[string]any{"limit": 1}, &first); err != nil {
		t.Fatalf("first request: %v", err)
	}

	store.revokeOnce()

	var second struct {
		Total int `json:"total"`
	}
	if err := c.post(t.Context(), "/api/search/product", map[string]any{"limit": 1}, &second); err != nil {
		t.Fatalf("request after revocation: %v", err)
	}

	if got := store.tokenRequests.Load(); got != 2 {
		t.Errorf("token requests = %d, want 2: one initial, one refresh", got)
	}
	if second.Total != 2 {
		t.Errorf("total = %d, want 2: the retry should have succeeded", second.Total)
	}
}

// Concurrent requests must share one token exchange. Providers that rotate
// credentials invalidate all but one of several simultaneous refreshes.
func TestConcurrentRequestsFetchOneToken(t *testing.T) {
	t.Parallel()

	store := newFakeStore(t)
	c := newTestClient(t, store)

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()

			var resp struct {
				Total int `json:"total"`
			}
			if err := c.post(t.Context(), "/api/search/product", map[string]any{"limit": 1}, &resp); err != nil {
				t.Errorf("concurrent request: %v", err)
			}
		}()
	}
	wg.Wait()

	if got := store.tokenRequests.Load(); got != 1 {
		t.Errorf("token requests = %d, want 1: concurrent callers must share one exchange", got)
	}
}

// store_summary counts three entities concurrently, which is why the tool
// exists rather than leaving an agent to make three calls.
func TestStoreSummaryCountsConcurrently(t *testing.T) {
	t.Parallel()

	store := newFakeStore(t)
	c := newTestClient(t, store)

	products, err := c.count(t.Context(), "product")
	if err != nil {
		t.Fatalf("count products: %v", err)
	}
	customers, err := c.count(t.Context(), "customer")
	if err != nil {
		t.Fatalf("count customers: %v", err)
	}

	if products != 2 {
		t.Errorf("products = %d, want 2", products)
	}
	if customers != 7 {
		t.Errorf("customers = %d, want 7", customers)
	}
}

func TestBindRequiresBaseURL(t *testing.T) {
	t.Parallel()

	_, err := New().Bind(t.Context(), &integrations.Binding{
		Params:     map[string]string{},
		Credential: credential.None{},
		HTTP:       http.DefaultClient,
	})
	if err == nil {
		t.Error("expected an error when base_url is missing")
	}
}

// Bind fills in the token endpoint from the store url, so an operator supplies
// only the store address.
func TestBindDerivesTokenURL(t *testing.T) {
	t.Parallel()

	cred := &credential.ClientCredentials{ClientID: "k", ClientSecret: "s"}

	if _, err := New().Bind(t.Context(), &integrations.Binding{
		Params:     map[string]string{"base_url": "https://shop.example.com/", "language": "de-DE"},
		Credential: cred,
		HTTP:       http.DefaultClient,
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}

	want := "https://shop.example.com/api/oauth/token"
	if cred.TokenURL != want {
		t.Errorf("TokenURL = %q, want %q", cred.TokenURL, want)
	}
}

func TestTokenURL(t *testing.T) {
	t.Parallel()

	for _, base := range []string{"https://shop.example.com", "https://shop.example.com/"} {
		if got := TokenURL(base); got != "https://shop.example.com/api/oauth/token" {
			t.Errorf("TokenURL(%q) = %q", base, got)
		}
	}
}

// Integration must satisfy the interface the registry consumes.
var _ integrations.Integration = (*Integration)(nil)
