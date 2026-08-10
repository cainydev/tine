package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/cainydev/tine/internal/credential"
	"github.com/cainydev/tine/internal/store/sqlc"
	"github.com/cainydev/tine/internal/web"
)

// UserBySubject returns the account for an OIDC subject.
func (s *Store) UserBySubject(ctx context.Context, subject string) (*web.User, error) {
	row, err := s.queries.GetUserBySubject(ctx, subject)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, web.ErrNoUser
	}
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}
	return &web.User{ID: row.ID, Subject: row.Subject, Slug: row.Slug, Email: row.Email}, nil
}

// CreateUser claims a username for a subject.
func (s *Store) CreateUser(ctx context.Context, subject, userSlug, email string) (*web.User, error) {
	id, err := newInstanceID()
	if err != nil {
		return nil, err
	}

	now := time.Now().Unix()
	row, err := s.queries.CreateUser(ctx, sqlc.CreateUserParams{
		ID: id, Subject: subject, Slug: userSlug, Email: email,
		CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	return &web.User{ID: row.ID, Subject: row.Subject, Slug: row.Slug, Email: row.Email}, nil
}

// SlugTaken reports whether a username is already in use.
func (s *Store) SlugTaken(ctx context.Context, userSlug string) (bool, error) {
	var found string
	err := s.db.QueryRowContext(ctx, `SELECT slug FROM users WHERE slug = ?`, userSlug).Scan(&found)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("check username: %w", err)
	default:
		return true, nil
	}
}

// InstancesForUser returns every instance a user owns.
func (s *Store) InstancesForUser(ctx context.Context, subject string) ([]web.Instance, error) {
	rows, err := s.queries.ListInstancesForUser(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}

	out := make([]web.Instance, 0, len(rows))
	for _, row := range rows {
		params, err := decodeParams(row.Params)
		if err != nil {
			return nil, err
		}
		out = append(out, web.Instance{
			ID:              row.ID,
			DisplayName:     row.DisplayName,
			IntegrationSlug: row.IntegrationSlug,
			IntegrationName: row.IntegrationName,
			Version:         row.IntegrationVersion,
			Params:          params,
			Enabled:         row.Enabled != 0,
			CredentialKind:  derefString(row.CredentialKind),
			NeedsReauth:     derefInt(row.CredentialNeedsReauth) != 0,
		})
	}
	return out, nil
}

// InstancesForIntegration returns a user's instances of one integration.
func (s *Store) InstancesForIntegration(ctx context.Context, subject, integrationSlug string) ([]web.Instance, error) {
	rows, err := s.queries.ListInstancesForUserIntegration(ctx, sqlc.ListInstancesForUserIntegrationParams{
		Subject: subject, Slug: integrationSlug,
	})
	if err != nil {
		return nil, fmt.Errorf("list instances: %w", err)
	}

	out := make([]web.Instance, 0, len(rows))
	for _, row := range rows {
		params, err := decodeParams(row.Params)
		if err != nil {
			return nil, err
		}
		out = append(out, web.Instance{
			ID:              row.ID,
			DisplayName:     row.DisplayName,
			IntegrationSlug: integrationSlug,
			Params:          params,
			Enabled:         row.Enabled != 0,
			CredentialKind:  derefString(row.CredentialKind),
			NeedsReauth:     derefInt(row.CredentialNeedsReauth) != 0,
		})
	}
	return out, nil
}

// InstanceForUser returns one instance, scoped to its owner.
func (s *Store) InstanceForUser(ctx context.Context, subject, id string) (*web.Instance, error) {
	row, err := s.queries.GetInstanceForUser(ctx, sqlc.GetInstanceForUserParams{
		Subject: subject, ID: id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil //nolint:nilnil // absence is not an error for the caller
	}
	if err != nil {
		return nil, fmt.Errorf("load instance: %w", err)
	}

	params, err := decodeParams(row.Params)
	if err != nil {
		return nil, err
	}

	return &web.Instance{
		ID:              row.ID,
		DisplayName:     row.DisplayName,
		IntegrationSlug: row.IntegrationSlug,
		IntegrationName: row.IntegrationName,
		Version:         row.IntegrationVersion,
		Params:          params,
		Enabled:         row.Enabled != 0,
		CredentialKind:  derefString(row.CredentialKind),
		NeedsReauth:     derefInt(row.CredentialNeedsReauth) != 0,
	}, nil
}

// CreateInstance creates an instance and returns its id.
func (s *Store) CreateInstance(ctx context.Context, in web.NewInstance) (string, error) {
	encoded, err := json.Marshal(in.Params)
	if err != nil {
		return "", fmt.Errorf("encode params: %w", err)
	}

	return s.SeedInstance(ctx, SeedRequest{
		Subject:            in.Subject,
		IntegrationSlug:    in.IntegrationSlug,
		IntegrationName:    in.IntegrationName,
		IntegrationVersion: in.Version,
		DisplayName:        in.DisplayName,
		Params:             string(encoded),
		Now:                time.Now().Unix(),
		NewID:              newInstanceID,
	})
}

// UpdateParams replaces an instance's settings.
func (s *Store) UpdateParams(ctx context.Context, subject, id string, params map[string]string) error {
	if err := s.assertOwner(ctx, subject, id); err != nil {
		return err
	}

	encoded, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode params: %w", err)
	}

	if err := s.queries.UpdateInstanceParams(ctx, sqlc.UpdateInstanceParamsParams{
		Params: string(encoded), UpdatedAt: time.Now().Unix(), ID: id,
	}); err != nil {
		return fmt.Errorf("update params: %w", err)
	}
	return nil
}

// DeleteInstance removes an instance and its credential.
func (s *Store) DeleteInstance(ctx context.Context, subject, id string) error {
	affected, err := s.queries.DeleteInstanceForUser(ctx, sqlc.DeleteInstanceForUserParams{
		ID: id, Subject: subject,
	})
	if err != nil {
		return fmt.Errorf("delete instance: %w", err)
	}
	if affected == 0 {
		return errors.New("instance not found")
	}
	return nil
}

// SetCredential seals and stores an instance's upstream credential.
func (s *Store) SetCredential(ctx context.Context, subject, id string, input web.CredentialInput) error {
	if err := s.assertOwner(ctx, subject, id); err != nil {
		return err
	}

	cred, err := buildCredential(input)
	if err != nil {
		return err
	}

	if cred.Kind() == credential.KindNone {
		if delErr := s.queries.DeleteCredential(ctx, id); delErr != nil {
			return fmt.Errorf("clear credential: %w", delErr)
		}
		return nil
	}

	credID, err := newInstanceID()
	if err != nil {
		return err
	}
	return s.SaveCredential(ctx, credID, id, cred, time.Now().Unix())
}

// buildCredential turns submitted form values into a credential.
func buildCredential(in web.CredentialInput) (credential.Credential, error) {
	switch credential.Kind(in.Kind) {
	case credential.KindNone, "":
		return credential.None{}, nil
	case credential.KindBearer:
		if in.Token == "" {
			return nil, errors.New("token is required")
		}
		return credential.Bearer{Token: in.Token}, nil
	case credential.KindHeader:
		if in.HeaderName == "" || in.Value == "" {
			return nil, errors.New("header name and value are required")
		}
		return credential.Header{Name: in.HeaderName, Value: in.Value}, nil
	case credential.KindBasic:
		if in.Username == "" || in.Password == "" {
			return nil, errors.New("username and password are required")
		}
		return credential.Basic{Username: in.Username, Password: in.Password}, nil
	case credential.KindOAuth2:
		return nil, errors.New("oauth is not supported through this form yet")
	default:
		return nil, fmt.Errorf("unknown credential type %q", in.Kind)
	}
}

// assertOwner fails unless the subject owns the instance.
func (s *Store) assertOwner(ctx context.Context, subject, id string) error {
	_, err := s.queries.GetInstanceForUser(ctx, sqlc.GetInstanceForUserParams{
		Subject: subject, ID: id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("instance not found")
	}
	if err != nil {
		return fmt.Errorf("check ownership: %w", err)
	}
	return nil
}

func decodeParams(raw string) (map[string]string, error) {
	if raw == "" {
		return map[string]string{}, nil
	}
	var out map[string]string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("decode params: %w", err)
	}
	return out, nil
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func derefInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// newInstanceID returns a short hex identifier.
//
// Hex so an id can never collide with a literal route segment such as "new".
func newInstanceID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// Store must satisfy the interface the web package consumes.
var _ web.Store = (*Store)(nil)
