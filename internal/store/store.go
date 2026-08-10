// Package store persists users, integrations, instances and credentials in
// SQLite, and resolves the instance addressed by a request path.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"

	"github.com/cainydev/tine/internal/credential"
	"github.com/cainydev/tine/internal/gateway"
	"github.com/cainydev/tine/internal/store/sqlc"

	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so CGO_ENABLED=0 still builds
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Store is the SQLite-backed persistence layer.
type Store struct {
	db      *sql.DB
	queries *sqlc.Queries

	sealer *credential.Sealer
}

// Open opens the database at path and applies any pending migrations.
//
// The pragmas are not optional. WAL lets readers proceed during a write, which
// is what makes a single-writer database usable for a concurrent proxy;
// busy_timeout stops a concurrent write from failing instantly with SQLITE_BUSY;
// foreign_keys is off by default in SQLite and must be enabled per connection.
func Open(ctx context.Context, path string) (*Store, error) {
	dsn := path + "?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("ping database: %w", err), db.Close())
	}

	s := &Store{db: db, queries: sqlc.New(db)}
	if err := s.migrate(ctx); err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// Resolve implements gateway.Resolver.
//
// All three path segments must match: a valid id under the wrong user or
// integration does not resolve. Disabled instances are treated as absent.
func (s *Store) Resolve(ctx context.Context, userSlug, integrationSlug, id string) (*gateway.Instance, error) {
	row, err := s.queries.ResolveInstance(ctx, sqlc.ResolveInstanceParams{
		ID:     id,
		Slug:   userSlug,
		Slug_2: integrationSlug,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gateway.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve instance: %w", err)
	}

	return &gateway.Instance{
		ID:              row.ID,
		OwnerSubject:    row.OwnerSubject,
		UserSlug:        row.UserSlug,
		IntegrationSlug: row.IntegrationSlug,
		DisplayName:     row.DisplayName,
		Version:         row.IntegrationVersion,
	}, nil
}
