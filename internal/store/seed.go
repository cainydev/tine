package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/cainydev/tine/internal/store/sqlc"
)

// SeedRequest describes a user, an integration and an instance to create
// together.
type SeedRequest struct {
	Subject  string
	UserSlug string
	Email    string

	IntegrationSlug    string
	IntegrationName    string
	IntegrationVersion string

	DisplayName string
	Params      string

	Now int64

	// NewID mints identifiers. Injected so tests can make them deterministic.
	NewID func() (string, error)
}

// SeedInstance creates the user and integration if absent, then one instance,
// returning the instance id.
//
// Everything happens in one transaction: a partial seed would leave a user with
// no instance and no clear way to tell whether the command succeeded.
func (s *Store) SeedInstance(ctx context.Context, req SeedRequest) (string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("begin: %w", err)
	}
	defer func() {
		// Rollback after Commit returns ErrTxDone, which is not a failure.
		if rbErr := tx.Rollback(); rbErr != nil && !errors.Is(rbErr, sql.ErrTxDone) {
			_ = rbErr
		}
	}()

	q := s.queries.WithTx(tx)

	user, err := q.GetUserBySubject(ctx, req.Subject)
	if errors.Is(err, sql.ErrNoRows) {
		id, idErr := req.NewID()
		if idErr != nil {
			return "", idErr
		}
		user, err = q.CreateUser(ctx, sqlc.CreateUserParams{
			ID: id, Subject: req.Subject, Slug: req.UserSlug,
			Email: req.Email, CreatedAt: req.Now, UpdatedAt: req.Now,
		})
	}
	if err != nil {
		return "", fmt.Errorf("user: %w", err)
	}

	integrationID, err := req.NewID()
	if err != nil {
		return "", err
	}
	integration, err := q.UpsertIntegration(ctx, sqlc.UpsertIntegrationParams{
		ID: integrationID, Slug: req.IntegrationSlug, Name: req.IntegrationName,
		Version: req.IntegrationVersion, CreatedAt: req.Now,
	})
	if err != nil {
		return "", fmt.Errorf("integration: %w", err)
	}

	instanceID, err := req.NewID()
	if err != nil {
		return "", err
	}
	if _, err := q.CreateInstance(ctx, sqlc.CreateInstanceParams{
		ID: instanceID, UserID: user.ID, IntegrationID: integration.ID,
		DisplayName: req.DisplayName, Params: req.Params,
		CreatedAt: req.Now, UpdatedAt: req.Now,
	}); err != nil {
		return "", fmt.Errorf("instance: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return instanceID, nil
}
