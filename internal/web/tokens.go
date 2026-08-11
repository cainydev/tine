package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/cainydev/tine/internal/web/views"
)

// expiryLayout is the value an <input type="date"> submits.
const expiryLayout = "2006-01-02"

func (s *Server) tokenList(w http.ResponseWriter, r *http.Request, u *User) error {
	return s.renderTokens(w, r, u, "", "")
}

// createToken issues a token and renders its plaintext, which is the only time
// it can be read.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request, u *User) error {
	if err := r.ParseForm(); err != nil {
		return fmt.Errorf("read form: %w", err)
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		return s.renderTokens(w, r, u, "", "a token needs a name")
	}

	expiresAt, err := parseExpiry(r.FormValue("expires_at"))
	if err != nil {
		return s.renderTokens(w, r, u, "", err.Error())
	}

	created, err := s.store.CreateToken(r.Context(), NewToken{
		Subject:     u.Subject,
		Name:        name,
		InstanceIDs: selectedInstances(r.Form["instance_id"]),
		ExpiresAt:   expiresAt,
	})
	if errors.Is(err, ErrNoInstance) {
		return s.renderTokens(w, r, u, "", "that instance does not exist")
	}
	if err != nil {
		return err
	}

	return s.renderTokens(w, r, u, created.Plaintext, "")
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, u *User) error {
	err := s.store.DeleteToken(r.Context(), u.Subject, r.PathValue("id"))
	if err != nil && !errors.Is(err, ErrNoToken) {
		return err
	}

	http.Redirect(w, r, "/settings/tokens", http.StatusSeeOther)
	return nil
}

// renderTokens draws the page. created is non-empty only immediately after
// issuing a token.
func (s *Server) renderTokens(w http.ResponseWriter, r *http.Request, u *User, created, failure string) error {
	ctx := r.Context()

	tokens, err := s.store.TokensForSubject(ctx, u.Subject)
	if err != nil {
		return err
	}

	instances, err := s.store.InstancesForUser(ctx, u.Subject)
	if err != nil {
		return err
	}

	s.render(w, r, views.Tokens(s.nav(u), viewTokens(tokens), viewScopes(instances), created, failure))
	return nil
}

// selectedInstances drops the empty option that means "every instance".
//
// The form always submits that option, so a selection containing it alongside
// real instances is read as the broader grant the user asked for.
func selectedInstances(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v == "" {
			return nil
		}
		out = append(out, v)
	}
	return out
}

func viewTokens(tokens []Token) []views.Token {
	out := make([]views.Token, 0, len(tokens))
	for _, t := range tokens {
		view := views.Token{
			ID:      t.ID,
			Name:    t.Name,
			Scoped:  t.Scoped,
			Created: "created " + t.CreatedAt.Format(expiryLayout),
		}
		for _, g := range t.Grants {
			view.Scopes = append(view.Scopes, fmt.Sprintf("%s · %s", g.IntegrationSlug, g.InstanceName))
		}
		if !t.ExpiresAt.IsZero() {
			view.Expires = t.ExpiresAt.Format(expiryLayout)
		}
		if !t.LastUsedAt.IsZero() {
			view.LastUsed = since(t.LastUsedAt, time.Now())
		}
		out = append(out, view)
	}
	return out
}

func viewScopes(instances []Instance) []views.TokenScope {
	out := make([]views.TokenScope, 0, len(instances))
	for _, in := range instances {
		out = append(out, views.TokenScope{
			InstanceID: in.ID,
			Label:      fmt.Sprintf("%s · %s", in.IntegrationSlug, in.DisplayName),
		})
	}
	return out
}

// since renders how long ago a token was used.
//
// Relative rather than a timestamp because the question a reader has is whether
// something is still using this token, which a date cannot answer. Use is
// recorded at most once a minute, so anything finer would be misleading.
func since(t, now time.Time) string {
	d := now.Sub(t)

	switch {
	case d < 2*time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 2*time.Hour:
		return "an hour ago"
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	case d < 48*time.Hour:
		return "yesterday"
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	default:
		return t.Format(expiryLayout)
	}
}

// parseExpiry reads the expiry field. Empty means the token never expires.
func parseExpiry(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, nil
	}

	day, err := time.ParseInLocation(expiryLayout, raw, time.Local)
	if err != nil {
		return time.Time{}, errors.New("expiry must be a date, as 2027-01-31")
	}

	// The field names a day, so the token stays valid through the end of it.
	end := day.Add(24 * time.Hour)
	if !end.After(time.Now()) {
		return time.Time{}, errors.New("expiry is in the past")
	}
	return end, nil
}
