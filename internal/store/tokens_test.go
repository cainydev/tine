package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/cainydev/tine/internal/gateway"
	"github.com/cainydev/tine/internal/store/sqlc"
	"github.com/cainydev/tine/internal/web"
)

// seedSecondUser adds another account with its own instance, for the checks
// that one user's token must not reach another's endpoint.
func seedSecondUser(t *testing.T, s *Store) string {
	t.Helper()
	ctx := t.Context()

	if _, err := s.queries.CreateUser(ctx, sqlc.CreateUserParams{
		ID: "user-2", Subject: "other-sub", Slug: "mallory",
		Email: "mallory@example.com", CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("create second user: %v", err)
	}

	if _, err := s.queries.CreateInstance(ctx, sqlc.CreateInstanceParams{
		ID: "inst-2", UserID: "user-2", IntegrationID: "integ-1",
		DisplayName: "Mallory Shopware", Params: `{}`,
		CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("create second instance: %v", err)
	}

	return "inst-2"
}

func TestCreateTokenReturnsPlaintextOnce(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	seed(t, s)

	created, err := s.CreateToken(t.Context(), web.NewToken{
		Subject: "owner-sub", Name: "nightly-sync",
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	if !strings.HasPrefix(created.Plaintext, TokenPrefix) {
		t.Errorf("plaintext = %q, want the %q prefix", created.Plaintext, TokenPrefix)
	}
	if len(created.Plaintext) < 40 {
		t.Errorf("plaintext is %d characters, want a token with real entropy", len(created.Plaintext))
	}

	// The listing must never expose the plaintext again.
	listed, err := s.TokensForSubject(t.Context(), "owner-sub")
	if err != nil {
		t.Fatalf("TokensForSubject: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d tokens, want 1", len(listed))
	}
	if listed[0].Name != "nightly-sync" {
		t.Errorf("name = %q, want %q", listed[0].Name, "nightly-sync")
	}
}

// TestTokenIsStoredHashed is the property that matters if the database leaks.
func TestTokenIsStoredHashed(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	seed(t, s)

	created, err := s.CreateToken(t.Context(), web.NewToken{Subject: "owner-sub", Name: "t"})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	var stored string
	if err := s.db.QueryRowContext(t.Context(), `SELECT hash FROM tokens`).Scan(&stored); err != nil {
		t.Fatalf("read stored hash: %v", err)
	}

	if stored == created.Plaintext {
		t.Fatal("the token is stored in plaintext")
	}
	if strings.Contains(stored, strings.TrimPrefix(created.Plaintext, TokenPrefix)) {
		t.Fatal("the stored hash contains the token")
	}
	if stored != hashToken(created.Plaintext) {
		t.Error("the stored value is not the token's sha-256")
	}
}

func TestVerifyToken(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	instanceID := seed(t, s)
	otherInstance := seedSecondUser(t, s)

	unscoped, err := s.CreateToken(t.Context(), web.NewToken{Subject: "owner-sub", Name: "all"})
	if err != nil {
		t.Fatalf("create unscoped: %v", err)
	}

	scoped, err := s.CreateToken(t.Context(), web.NewToken{
		Subject: "owner-sub", Name: "one", InstanceIDs: []string{instanceID},
	})
	if err != nil {
		t.Fatalf("create scoped: %v", err)
	}

	expired, err := s.CreateToken(t.Context(), web.NewToken{
		Subject: "owner-sub", Name: "expired",
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatalf("create expired: %v", err)
	}

	tests := []struct {
		name       string
		token      string
		instanceID string
		wantSubID  string
		wantErr    bool
	}{
		{
			name:       "unscoped token reaches its owner's instance",
			token:      unscoped.Plaintext,
			instanceID: instanceID,
			wantSubID:  "owner-sub",
		},
		{
			name:       "scoped token reaches the instance it names",
			token:      scoped.Plaintext,
			instanceID: instanceID,
			wantSubID:  "owner-sub",
		},
		{
			name:       "scoped token is rejected at another instance",
			token:      scoped.Plaintext,
			instanceID: otherInstance,
			wantErr:    true,
		},
		{
			name:       "expired token is rejected",
			token:      expired.Plaintext,
			instanceID: instanceID,
			wantErr:    true,
		},
		{
			name:       "unknown token is rejected",
			token:      TokenPrefix + "totallymadeupvalue",
			instanceID: instanceID,
			wantErr:    true,
		},
		{
			name:       "a value without the prefix is rejected",
			token:      "eyJhbGciOiJSUzI1NiJ9.fake.jwt",
			instanceID: instanceID,
			wantErr:    true,
		},
		{
			name:       "empty is rejected",
			token:      "",
			instanceID: instanceID,
			wantErr:    true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := s.VerifyToken(t.Context(), tc.token, tc.instanceID)

			if tc.wantErr {
				if err == nil {
					t.Fatalf("VerifyToken accepted the token, returned %q", got)
				}
				if !errors.Is(err, gateway.ErrUnauthenticated) {
					t.Errorf("error = %v, want it to wrap ErrUnauthenticated", err)
				}
				return
			}

			if err != nil {
				t.Fatalf("VerifyToken: %v", err)
			}
			if got != tc.wantSubID {
				t.Errorf("subject = %q, want %q", got, tc.wantSubID)
			}
		})
	}
}

// TestUnscopedTokenDoesNotCrossUsers records that the subject, not the token,
// is what stops one user reaching another's endpoint. VerifyToken returns the
// owner's subject; the gateway compares it against the instance owner.
func TestUnscopedTokenDoesNotCrossUsers(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	seed(t, s)
	otherInstance := seedSecondUser(t, s)

	created, err := s.CreateToken(t.Context(), web.NewToken{Subject: "owner-sub", Name: "all"})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	subject, err := s.VerifyToken(t.Context(), created.Plaintext, otherInstance)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if subject == "other-sub" {
		t.Fatal("the token authenticated as the other user")
	}
	if subject != "owner-sub" {
		t.Errorf("subject = %q, want %q", subject, "owner-sub")
	}
}

func TestCreateTokenRejectsAnotherUsersInstance(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	seed(t, s)
	otherInstance := seedSecondUser(t, s)

	_, err := s.CreateToken(t.Context(), web.NewToken{
		Subject: "owner-sub", Name: "steal", InstanceIDs: []string{otherInstance},
	})
	if !errors.Is(err, web.ErrNoInstance) {
		t.Fatalf("error = %v, want ErrNoInstance", err)
	}
}

// TestCreateTokenForUnknownSubject covers the CLI path: the web interface
// always has an account, but `tine connect` can name any subject.
func TestCreateTokenForUnknownSubject(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	seed(t, s)

	_, err := s.CreateToken(t.Context(), web.NewToken{Subject: "ghost-subject", Name: "x"})
	if !errors.Is(err, web.ErrNoUser) {
		t.Fatalf("error = %v, want ErrNoUser", err)
	}
}

func TestDeleteTokenRevokesAccess(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	instanceID := seed(t, s)

	created, err := s.CreateToken(t.Context(), web.NewToken{Subject: "owner-sub", Name: "temp"})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	if _, err := s.VerifyToken(t.Context(), created.Plaintext, instanceID); err != nil {
		t.Fatalf("token should verify before revocation: %v", err)
	}

	if err := s.DeleteToken(t.Context(), "owner-sub", created.ID); err != nil {
		t.Fatalf("DeleteToken: %v", err)
	}

	if _, err := s.VerifyToken(t.Context(), created.Plaintext, instanceID); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Fatalf("error = %v, want the revoked token to be rejected", err)
	}
}

func TestDeleteTokenIsScopedToItsOwner(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	instanceID := seed(t, s)
	seedSecondUser(t, s)

	created, err := s.CreateToken(t.Context(), web.NewToken{Subject: "owner-sub", Name: "mine"})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	if err := s.DeleteToken(t.Context(), "other-sub", created.ID); !errors.Is(err, web.ErrNoToken) {
		t.Fatalf("error = %v, want ErrNoToken", err)
	}

	if _, verifyErr := s.VerifyToken(t.Context(), created.Plaintext, instanceID); verifyErr != nil {
		t.Errorf("another user's delete revoked the token: %v", verifyErr)
	}
}

func TestTokensForSubjectIsScoped(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	seed(t, s)
	seedSecondUser(t, s)

	if _, err := s.CreateToken(t.Context(), web.NewToken{Subject: "owner-sub", Name: "mine"}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if _, err := s.CreateToken(t.Context(), web.NewToken{Subject: "other-sub", Name: "theirs"}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	listed, err := s.TokensForSubject(t.Context(), "owner-sub")
	if err != nil {
		t.Fatalf("TokensForSubject: %v", err)
	}
	if len(listed) != 1 {
		t.Fatalf("listed %d tokens, want only the caller's", len(listed))
	}
	if listed[0].Name != "mine" {
		t.Errorf("name = %q, want %q", listed[0].Name, "mine")
	}
}

// TestTokenScopeIsReported covers the listing showing which endpoint a scoped
// token reaches, which is what makes a revoke decision possible.
func TestTokenScopeIsReported(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	instanceID := seed(t, s)

	if _, err := s.CreateToken(t.Context(), web.NewToken{
		Subject: "owner-sub", Name: "scoped", InstanceIDs: []string{instanceID},
	}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	if _, err := s.CreateToken(t.Context(), web.NewToken{Subject: "owner-sub", Name: "unscoped"}); err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	listed, err := s.TokensForSubject(t.Context(), "owner-sub")
	if err != nil {
		t.Fatalf("TokensForSubject: %v", err)
	}

	byName := make(map[string]web.Token, len(listed))
	for _, tok := range listed {
		byName[tok.Name] = tok
	}

	scopedToken := byName["scoped"]
	if len(scopedToken.Grants) != 1 {
		t.Fatalf("scoped token has %d grants, want 1", len(scopedToken.Grants))
	}
	if got := scopedToken.Grants[0]; got.InstanceID != instanceID || got.IntegrationSlug != "shopware" {
		t.Errorf("grant = %+v, want it to name its instance and integration", got)
	}

	if got := byName["unscoped"]; len(got.Grants) != 0 {
		t.Errorf("unscoped token names %d instances, want none", len(got.Grants))
	}
}

// TestTokenGrantsASubset is the reason grants are a table rather than a column:
// one token reaches some of a user's instances but not all.
func TestTokenGrantsASubset(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	first := seed(t, s)

	if _, err := s.queries.CreateInstance(t.Context(), sqlc.CreateInstanceParams{
		ID: "inst-3", UserID: "user-1", IntegrationID: "integ-1",
		DisplayName: "Shopware Staging", Params: `{}`,
		CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("create second instance: %v", err)
	}

	if _, err := s.queries.CreateInstance(t.Context(), sqlc.CreateInstanceParams{
		ID: "inst-4", UserID: "user-1", IntegrationID: "integ-1",
		DisplayName: "Shopware Sandbox", Params: `{}`,
		CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("create third instance: %v", err)
	}

	created, err := s.CreateToken(t.Context(), web.NewToken{
		Subject: "owner-sub", Name: "two-of-three",
		InstanceIDs: []string{first, "inst-3"},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	for _, granted := range []string{first, "inst-3"} {
		if _, verifyErr := s.VerifyToken(t.Context(), created.Plaintext, granted); verifyErr != nil {
			t.Errorf("granted instance %s was rejected: %v", granted, verifyErr)
		}
	}

	if _, verifyErr := s.VerifyToken(t.Context(), created.Plaintext, "inst-4"); !errors.Is(verifyErr, gateway.ErrUnauthenticated) {
		t.Errorf("error = %v, want the ungranted instance to be rejected", verifyErr)
	}

	listed, err := s.TokensForSubject(t.Context(), "owner-sub")
	if err != nil {
		t.Fatalf("TokensForSubject: %v", err)
	}
	if len(listed[0].Grants) != 2 {
		t.Errorf("listed %d grants, want 2", len(listed[0].Grants))
	}
}

// TestPartialGrantFailureLeavesNoToken covers the transaction: a token whose
// grants could not all be written must not exist at all, because a token with
// a truncated scope is a token with the wrong permissions.
func TestPartialGrantFailureLeavesNoToken(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	instanceID := seed(t, s)
	otherInstance := seedSecondUser(t, s)

	_, err := s.CreateToken(t.Context(), web.NewToken{
		Subject: "owner-sub", Name: "partial",
		InstanceIDs: []string{instanceID, otherInstance},
	})
	if !errors.Is(err, web.ErrNoInstance) {
		t.Fatalf("error = %v, want ErrNoInstance", err)
	}

	listed, err := s.TokensForSubject(t.Context(), "owner-sub")
	if err != nil {
		t.Fatalf("TokensForSubject: %v", err)
	}
	if len(listed) != 0 {
		t.Errorf("listed %d tokens, want the rejected one not to exist", len(listed))
	}
}

func TestDeletingAnInstanceRevokesItsTokens(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	instanceID := seed(t, s)

	created, err := s.CreateToken(t.Context(), web.NewToken{
		Subject: "owner-sub", Name: "scoped", InstanceIDs: []string{instanceID},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	if err := s.DeleteInstance(t.Context(), "owner-sub", instanceID); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}

	if _, err := s.VerifyToken(t.Context(), created.Plaintext, instanceID); !errors.Is(err, gateway.ErrUnauthenticated) {
		t.Fatalf("error = %v, want the token to die with its instance", err)
	}
}

// TestDeletedInstanceDoesNotWidenAToken pins the reason a token records that it
// is scoped rather than inferring it from its grants: deleting an instance
// cascades its grants away, and an empty grant set must not read as "every
// instance".
func TestDeletedInstanceDoesNotWidenAToken(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	instanceID := seed(t, s)

	if _, err := s.queries.CreateInstance(t.Context(), sqlc.CreateInstanceParams{
		ID: "inst-5", UserID: "user-1", IntegrationID: "integ-1",
		DisplayName: "Untouched", Params: `{}`, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatalf("create second instance: %v", err)
	}

	created, err := s.CreateToken(t.Context(), web.NewToken{
		Subject: "owner-sub", Name: "scoped", InstanceIDs: []string{instanceID},
	})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	if err := s.DeleteInstance(t.Context(), "owner-sub", instanceID); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}

	if _, verifyErr := s.VerifyToken(t.Context(), created.Plaintext, "inst-5"); !errors.Is(verifyErr, gateway.ErrUnauthenticated) {
		t.Fatalf("error = %v, want the token not to widen to an instance it never named", verifyErr)
	}
}

func TestTouchTokenRecordsUse(t *testing.T) {
	t.Parallel()

	s := openTestStore(t)
	instanceID := seed(t, s)

	created, err := s.CreateToken(t.Context(), web.NewToken{Subject: "owner-sub", Name: "used"})
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	listed, err := s.TokensForSubject(t.Context(), "owner-sub")
	if err != nil {
		t.Fatalf("TokensForSubject: %v", err)
	}
	if !listed[0].LastUsedAt.IsZero() {
		t.Fatal("a fresh token reports use")
	}

	if _, verifyErr := s.VerifyToken(t.Context(), created.Plaintext, instanceID); verifyErr != nil {
		t.Fatalf("VerifyToken: %v", verifyErr)
	}

	listed, err = s.TokensForSubject(t.Context(), "owner-sub")
	if err != nil {
		t.Fatalf("TokensForSubject: %v", err)
	}
	if listed[0].LastUsedAt.IsZero() {
		t.Error("use was not recorded")
	}
}
