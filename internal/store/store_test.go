package store

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/cainydev/tine/internal/gateway"
	"github.com/cainydev/tine/internal/store/sqlc"
)

// openTestStore returns a Store backed by a real file in a temp directory.
// A file rather than :memory: because tine sets MaxOpenConns(1) and the
// migration path should be exercised exactly as it runs in production.
func openTestStore(t *testing.T) *Store {
	t.Helper()

	s, err := Open(t.Context(), filepath.Join(t.TempDir(), "tine.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close store: %v", err)
		}
	})
	return s
}

// seed creates one user, one integration and one instance, returning the
// instance id.
func seed(t *testing.T, s *Store) string {
	t.Helper()
	ctx := t.Context()

	if _, err := s.queries.CreateUser(ctx, sqlc.CreateUserParams{
		ID: "user-1", Subject: "owner-sub", Slug: "john",
		Email: "john@example.com", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("create user: %v", err)
	}

	if _, err := s.queries.UpsertIntegration(ctx, sqlc.UpsertIntegrationParams{
		ID: "integ-1", Slug: "shopware", Name: "Shopware 6",
		Version: "1.0.0", CreatedAt: 1,
	}); err != nil {
		t.Fatalf("upsert integration: %v", err)
	}

	if _, err := s.queries.CreateInstance(ctx, sqlc.CreateInstanceParams{
		ID: "inst-1", UserID: "user-1", IntegrationID: "integ-1",
		DisplayName: "Shopware Production", Params: `{"scope":"read"}`,
		CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	return "inst-1"
}

func TestOpenIsIdempotent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "tine.db")

	for i := range 2 {
		s, err := Open(t.Context(), path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if err := s.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}
}

func TestResolve(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	id := seed(t, s)

	tests := []struct {
		name        string
		userSlug    string
		integration string
		id          string
		wantErr     error
	}{
		{"exact match", "john", "shopware", id, nil},
		{"unknown id", "john", "shopware", "nope", gateway.ErrNotFound},
		// The id alone must not be enough: the whole path identifies the instance.
		{"wrong user", "nobody", "shopware", id, gateway.ErrNotFound},
		{"wrong integration", "john", "billbee", id, gateway.ErrNotFound},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			inst, err := s.Resolve(t.Context(), tc.userSlug, tc.integration, tc.id)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if inst.ID != tc.id {
				t.Errorf("ID = %q, want %q", inst.ID, tc.id)
			}
			if inst.OwnerSubject != "owner-sub" {
				t.Errorf("OwnerSubject = %q, want %q", inst.OwnerSubject, "owner-sub")
			}
			if inst.IntegrationSlug != "shopware" {
				t.Errorf("IntegrationSlug = %q", inst.IntegrationSlug)
			}
			if inst.Version != "1.0.0" {
				t.Errorf("Version = %q, want 1.0.0", inst.Version)
			}
		})
	}
}

// A disabled instance must resolve as absent, so operators can switch an
// endpoint off without deleting it.
func TestResolveSkipsDisabled(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	id := seed(t, s)

	if err := s.queries.SetInstanceEnabled(t.Context(), sqlc.SetInstanceEnabledParams{
		Enabled: 0, UpdatedAt: 2, ID: id,
	}); err != nil {
		t.Fatalf("disable instance: %v", err)
	}

	if _, err := s.Resolve(t.Context(), "john", "shopware", id); !errors.Is(err, gateway.ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, gateway.ErrNotFound)
	}
}

// Deleting an instance must take its credential with it; a stale credential row
// would outlive the endpoint that scoped it.
func TestCredentialCascade(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	id := seed(t, s)
	ctx := t.Context()

	if _, err := s.queries.UpsertCredential(ctx, sqlc.UpsertCredentialParams{
		ID: "cred-1", InstanceID: id, Kind: "bearer",
		Ciphertext: []byte("sealed"), Nonce: []byte("nonce"), KeyID: "key-1",
		CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("upsert credential: %v", err)
	}

	if err := s.queries.DeleteInstance(ctx, id); err != nil {
		t.Fatalf("delete instance: %v", err)
	}

	_, err := s.queries.GetCredentialByInstance(ctx, id)
	if err == nil {
		t.Fatal("credential outlived its instance; ON DELETE CASCADE is not in effect")
	}
}

// Store must satisfy the interface the gateway consumes.
var _ gateway.Resolver = (*Store)(nil)
