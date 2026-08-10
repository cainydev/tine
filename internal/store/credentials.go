package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cainydev/tine/internal/credential"
	"github.com/cainydev/tine/internal/store/sqlc"
)

// ErrNeedsReauth is returned when a credential was rejected upstream and could
// not be refreshed. The endpoint still resolves, so the caller gets a clear
// reason rather than a generic failure.
var ErrNeedsReauth = errors.New("credential needs reauthorisation")

// WithSealer returns a copy of the store that can seal and open credentials.
//
// The sealer is separate from Open so that the database can be migrated and
// inspected without holding key material.
func (s *Store) WithSealer(sealer *credential.Sealer) *Store {
	clone := *s
	clone.sealer = sealer
	return &clone
}

// LoadParams returns an instance's configured settings.
func (s *Store) LoadParams(ctx context.Context, instanceID string) (map[string]string, error) {
	raw, err := s.queries.GetInstanceParams(ctx, instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("instance %s: %w", instanceID, sql.ErrNoRows)
	}
	if err != nil {
		return nil, fmt.Errorf("load params: %w", err)
	}

	var params map[string]string
	if raw == "" {
		return map[string]string{}, nil
	}
	if err := json.Unmarshal([]byte(raw), &params); err != nil {
		return nil, fmt.Errorf("decode params for instance %s: %w", instanceID, err)
	}
	return params, nil
}

// LoadCredential returns the credential bound to an instance.
//
// An instance with no credential row gets a no-auth credential rather than an
// error: integrations that need no authentication are ordinary, not exceptional.
func (s *Store) LoadCredential(ctx context.Context, instanceID string) (credential.Credential, error) {
	row, err := s.queries.GetCredentialByInstance(ctx, instanceID)
	if errors.Is(err, sql.ErrNoRows) {
		return credential.None{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load credential: %w", err)
	}

	if row.NeedsReauth != 0 {
		return nil, fmt.Errorf("instance %s: %w", instanceID, ErrNeedsReauth)
	}

	if s.sealer == nil {
		return nil, errors.New("store has no sealer; credentials cannot be opened")
	}

	plaintext, err := s.sealer.Open(&credential.Sealed{
		Ciphertext: row.Ciphertext,
		Nonce:      row.Nonce,
		KeyID:      row.KeyID,
	})
	if err != nil {
		return nil, fmt.Errorf("open credential for instance %s: %w", instanceID, err)
	}

	cred, err := credential.Unmarshal(credential.Kind(row.Kind), plaintext)
	if err != nil {
		return nil, fmt.Errorf("decode credential for instance %s: %w", instanceID, err)
	}
	return cred, nil
}

// SaveCredential seals and stores the credential for an instance.
func (s *Store) SaveCredential(ctx context.Context, id, instanceID string, cred credential.Credential, now int64) error {
	if s.sealer == nil {
		return errors.New("store has no sealer; credentials cannot be sealed")
	}

	plaintext, err := credential.Marshal(cred)
	if err != nil {
		return fmt.Errorf("encode credential: %w", err)
	}

	sealed, err := s.sealer.Seal(plaintext)
	if err != nil {
		return fmt.Errorf("seal credential: %w", err)
	}

	_, err = s.queries.UpsertCredential(ctx, sqlc.UpsertCredentialParams{
		ID:         id,
		InstanceID: instanceID,
		Kind:       string(cred.Kind()),
		Ciphertext: sealed.Ciphertext,
		Nonce:      sealed.Nonce,
		KeyID:      sealed.KeyID,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
	if err != nil {
		return fmt.Errorf("store credential: %w", err)
	}
	return nil
}
