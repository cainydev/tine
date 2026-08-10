package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// migrate applies every migration not yet recorded, in filename order.
func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at INTEGER NOT NULL
		) STRICT`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}

	for _, name := range names {
		applied, err := s.isApplied(ctx, name)
		if err != nil {
			return err
		}
		if applied {
			continue
		}
		if err := s.apply(ctx, name); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) isApplied(ctx context.Context, name string) (bool, error) {
	var found string
	err := s.db.QueryRowContext(ctx,
		`SELECT name FROM schema_migrations WHERE name = ?`, name).Scan(&found)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("check migration %s: %w", name, err)
	default:
		return true, nil
	}
}

// apply runs one migration and records it in the same transaction, so a failure
// part-way through leaves neither the schema change nor the record behind.
func (s *Store) apply(ctx context.Context, name string) error {
	body, err := fs.ReadFile(migrationFS, name)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	up, err := upSection(string(body))
	if err != nil {
		return err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			_ = err
		}
	}()

	if _, err := tx.ExecContext(ctx, up); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (name, applied_at) VALUES (?, unixepoch())`, name); err != nil {
		return fmt.Errorf("record: %w", err)
	}
	return tx.Commit()
}

// upSection returns the statements between the goose Up and Down markers.
func upSection(body string) (string, error) {
	const (
		upMarker   = "-- +goose Up"
		downMarker = "-- +goose Down"
	)

	start := strings.Index(body, upMarker)
	if start < 0 {
		return "", errors.New("migration has no `-- +goose Up` marker")
	}
	up := body[start+len(upMarker):]

	if end := strings.Index(up, downMarker); end >= 0 {
		up = up[:end]
	}

	up = strings.TrimSpace(up)
	if up == "" {
		return "", errors.New("migration has an empty Up section")
	}
	return up, nil
}
