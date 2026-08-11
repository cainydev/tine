package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"modernc.org/sqlite"

	"github.com/cainydev/tine/internal/gateway"
	"github.com/cainydev/tine/internal/store/sqlc"
	"github.com/cainydev/tine/internal/web"
)

const (
	// TokenPrefix makes a leaked token recognisable in a log or a repository
	// scan, and lets verification reject a foreign value without a query.
	TokenPrefix = gateway.TokenPrefix

	tokenBytes = 32

	// touchInterval bounds how often last_used_at is rewritten, so a token in
	// constant use does not serialise requests behind a write.
	touchInterval = time.Minute

	// sqliteConstraintForeignKey is SQLITE_CONSTRAINT_FOREIGNKEY.
	sqliteConstraintForeignKey = 787
)

// CreateToken issues a bearer token for subject and returns it in plaintext.
//
// The plaintext is returned exactly once and never stored; only its hash is
// persisted, so a copy of the database yields no usable credential.
func (s *Store) CreateToken(ctx context.Context, in web.NewToken) (*web.CreatedToken, error) {
	plaintext, err := generateToken()
	if err != nil {
		return nil, err
	}

	id, err := newInstanceID()
	if err != nil {
		return nil, err
	}

	for _, instanceID := range in.InstanceIDs {
		owned, ownErr := s.InstanceForUser(ctx, in.Subject, instanceID)
		if ownErr != nil {
			return nil, ownErr
		}
		if owned == nil {
			return nil, web.ErrNoInstance
		}
	}

	var expiresAt *int64
	if !in.ExpiresAt.IsZero() {
		unix := in.ExpiresAt.Unix()
		expiresAt = &unix
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			_ = rollbackErr
		}
	}()

	q := s.queries.WithTx(tx)

	scoped := int64(0)
	if len(in.InstanceIDs) > 0 {
		scoped = 1
	}

	row, err := q.CreateToken(ctx, sqlc.CreateTokenParams{
		ID:        id,
		Subject:   in.Subject,
		Name:      in.Name,
		Hash:      hashToken(plaintext),
		Scoped:    scoped,
		ExpiresAt: expiresAt,
		CreatedAt: time.Now().Unix(),
	})
	if err != nil {
		// The subject foreign key is the only one reachable here: an instance
		// is checked above, and it cannot exist without its owner's account.
		var sqliteErr *sqlite.Error
		if errors.As(err, &sqliteErr) && sqliteErr.Code() == sqliteConstraintForeignKey {
			return nil, web.ErrNoUser
		}
		return nil, fmt.Errorf("create token: %w", err)
	}

	for _, instanceID := range in.InstanceIDs {
		if err := q.GrantTokenInstance(ctx, sqlc.GrantTokenInstanceParams{
			TokenID: row.ID, InstanceID: instanceID,
		}); err != nil {
			return nil, fmt.Errorf("grant instance %s: %w", instanceID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	return &web.CreatedToken{ID: row.ID, Name: row.Name, Plaintext: plaintext}, nil
}

// TokensForSubject lists a user's tokens. The plaintext is not recoverable.
func (s *Store) TokensForSubject(ctx context.Context, subject string) ([]web.Token, error) {
	rows, err := s.queries.ListTokensForSubject(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("list tokens: %w", err)
	}

	grants, err := s.queries.ListGrantsForSubject(ctx, subject)
	if err != nil {
		return nil, fmt.Errorf("list token grants: %w", err)
	}

	byToken := make(map[string][]web.TokenGrant, len(rows))
	for _, g := range grants {
		byToken[g.TokenID] = append(byToken[g.TokenID], web.TokenGrant{
			InstanceID:      g.InstanceID,
			InstanceName:    g.InstanceName,
			IntegrationSlug: g.IntegrationSlug,
		})
	}

	out := make([]web.Token, 0, len(rows))
	for _, row := range rows {
		t := web.Token{
			ID:        row.ID,
			Name:      row.Name,
			Scoped:    row.Scoped != 0,
			Grants:    byToken[row.ID],
			CreatedAt: time.Unix(row.CreatedAt, 0),
		}
		if row.ExpiresAt != nil {
			t.ExpiresAt = time.Unix(*row.ExpiresAt, 0)
		}
		if row.LastUsedAt != nil {
			t.LastUsedAt = time.Unix(*row.LastUsedAt, 0)
		}
		out = append(out, t)
	}
	return out, nil
}

// DeleteToken revokes a token. Scoped to subject so one user cannot revoke
// another's.
func (s *Store) DeleteToken(ctx context.Context, subject, id string) error {
	affected, err := s.queries.DeleteToken(ctx, sqlc.DeleteTokenParams{ID: id, Subject: subject})
	if err != nil {
		return fmt.Errorf("delete token: %w", err)
	}
	if affected == 0 {
		return web.ErrNoToken
	}
	return nil
}

// VerifyToken implements gateway.TokenVerifier.
//
// It returns the subject a token authenticates as, and reports whether the
// token may reach the instance at path.
func (s *Store) VerifyToken(ctx context.Context, plaintext, instanceID string) (string, error) {
	if !strings.HasPrefix(plaintext, TokenPrefix) {
		return "", gateway.ErrUnauthenticated
	}

	row, err := s.queries.GetTokenByHash(ctx, hashToken(plaintext))
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: no such token", gateway.ErrUnauthenticated)
	}
	if err != nil {
		return "", fmt.Errorf("look up token: %w", err)
	}

	now := time.Now()
	if row.ExpiresAt != nil && now.Unix() >= *row.ExpiresAt {
		return "", fmt.Errorf("%w: token expired", gateway.ErrUnauthenticated)
	}

	if row.Scoped != 0 {
		granted, grantErr := s.queries.TokenGrantsInstance(ctx, sqlc.TokenGrantsInstanceParams{
			TokenID: row.ID, InstanceID: instanceID,
		})
		if grantErr != nil {
			return "", fmt.Errorf("read token grants: %w", grantErr)
		}
		if granted == 0 {
			return "", fmt.Errorf("%w: token does not grant this instance", gateway.ErrUnauthenticated)
		}
	}

	s.touchToken(ctx, row.ID, row.LastUsedAt, now)

	return row.Subject, nil
}

// touchToken records use, at most once per touchInterval.
//
// A failure here is not worth failing the request over: last_used_at is
// reporting, not authorisation.
func (s *Store) touchToken(ctx context.Context, id string, lastUsed *int64, now time.Time) {
	if lastUsed != nil && now.Sub(time.Unix(*lastUsed, 0)) < touchInterval {
		return
	}

	cutoff := now.Add(-touchInterval).Unix()
	if err := s.queries.TouchToken(ctx, sqlc.TouchTokenParams{
		LastUsedAt:   ptr(now.Unix()),
		ID:           id,
		LastUsedAt_2: &cutoff,
	}); err != nil {
		// Deliberately ignored: recording use must not fail a valid request.
		_ = err
	}
}

func ptr[T any](v T) *T { return &v }

// generateToken returns a new token in plaintext.
func generateToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return TokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// hashToken returns the stored form of a token.
//
// SHA-256 without a salt is right here, unlike for a password: a token carries
// full entropy, so there is no dictionary to attack, and verification has to
// find the row by hash in one indexed lookup.
func hashToken(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}
