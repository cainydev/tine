package web

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

const (
	sessionCookie = "tine_session"
	stateCookie   = "tine_state"

	sessionLifetime = 30 * 24 * time.Hour
	stateLifetime   = 10 * time.Minute
)

// ErrNoSession is returned when a request carries no usable session.
var ErrNoSession = errors.New("no session")

// Authenticator signs users in to the web interface.
type Authenticator struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth    *oauth2.Config

	secret []byte

	secureCookies bool

	authParams []oauth2.AuthCodeOption
}

// AuthConfig configures the web session layer.
type AuthConfig struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string

	Secret []byte

	SecureCookies bool

	AuthParams map[string]string
}

// NewAuthenticator discovers the issuer and returns an Authenticator.
func NewAuthenticator(ctx context.Context, cfg AuthConfig) (*Authenticator, error) {
	switch {
	case cfg.Issuer == "":
		return nil, errors.New("oidc issuer is required")
	case cfg.ClientID == "":
		return nil, errors.New("oidc client id is required")
	case len(cfg.Secret) < 32:
		return nil, errors.New("session secret must be at least 32 bytes")
	}

	provider, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, fmt.Errorf("discover oidc issuer %q: %w", cfg.Issuer, err)
	}

	return &Authenticator{
		provider: provider,
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: &oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  cfg.RedirectURL,
			Scopes:       []string{oidc.ScopeOpenID, "profile", "email"},
		},
		secret:        cfg.Secret,
		secureCookies: cfg.SecureCookies,
		authParams:    authCodeOptions(cfg),
	}, nil
}

// authCodeOptions builds the extra authorization parameters for a provider.
//
// WorkOS is defaulted because its authorization endpoint rejects a request with
// no connection selector, which is not obvious from the error it returns.
func authCodeOptions(cfg AuthConfig) []oauth2.AuthCodeOption {
	params := cfg.AuthParams
	if len(params) == 0 && strings.Contains(cfg.Issuer, "api.workos.com") {
		params = map[string]string{"provider": "authkit"}
	}

	out := make([]oauth2.AuthCodeOption, 0, len(params))
	for k, v := range params {
		out = append(out, oauth2.SetAuthURLParam(k, v))
	}
	return out
}

// Session is an authenticated web user.
type Session struct {
	Subject string
	Email   string
}

// Start sends a browser to the identity provider.
func (a *Authenticator) Start(w http.ResponseWriter, r *http.Request) (string, error) {
	state, err := randomString()
	if err != nil {
		return "", err
	}

	verifier := oauth2.GenerateVerifier()

	payload := state + "|" + verifier
	a.setCookie(w, stateCookie, a.sign(payload), stateLifetime)

	opts := append([]oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}, a.authParams...)
	return a.oauth.AuthCodeURL(state, opts...), nil
}

// Complete exchanges an authorization code for a session and sets the cookie.
func (a *Authenticator) Complete(ctx context.Context, w http.ResponseWriter, r *http.Request) (*Session, error) {
	cookie, err := r.Cookie(stateCookie)
	if err != nil {
		return nil, errors.New("login state is missing, start again")
	}
	a.clearCookie(w, stateCookie)

	payload, ok := a.verify(cookie.Value)
	if !ok {
		return nil, errors.New("login state is invalid, start again")
	}

	state, verifier, ok := strings.Cut(payload, "|")
	if !ok {
		return nil, errors.New("login state is malformed, start again")
	}
	if state != r.URL.Query().Get("state") {
		return nil, errors.New("login state does not match, start again")
	}

	token, err := a.oauth.Exchange(ctx, r.URL.Query().Get("code"),
		oauth2.VerifierOption(verifier),
	)
	if err != nil {
		return nil, fmt.Errorf("exchange authorization code: %w", err)
	}

	session, err := a.sessionFromToken(ctx, token)
	if err != nil {
		return nil, err
	}

	if err := a.setSession(w, session); err != nil {
		return nil, err
	}
	return session, nil
}

// sessionFromToken reads the signed-in identity out of a token response.
func (a *Authenticator) sessionFromToken(ctx context.Context, token *oauth2.Token) (*Session, error) {
	if raw, ok := token.Extra("id_token").(string); ok && raw != "" {
		verified, err := a.verifier.Verify(ctx, raw)
		if err != nil {
			return nil, fmt.Errorf("verify id token: %w", err)
		}

		var claims struct {
			Email string `json:"email"`
		}
		if err := verified.Claims(&claims); err != nil {
			return nil, fmt.Errorf("read id token claims: %w", err)
		}
		return &Session{Subject: verified.Subject, Email: claims.Email}, nil
	}

	if token.AccessToken == "" {
		return nil, errors.New("provider returned neither an id token nor an access token")
	}

	verifier := a.provider.Verifier(&oidc.Config{SkipClientIDCheck: true})

	verified, err := verifier.Verify(ctx, token.AccessToken)
	if err != nil {
		return nil, fmt.Errorf("verify access token: %w", err)
	}

	var claims struct {
		Email string `json:"email"`
	}
	if err := verified.Claims(&claims); err != nil {
		return nil, fmt.Errorf("read access token claims: %w", err)
	}

	if claims.Email == "" {
		if email, ok := token.Extra("email").(string); ok {
			claims.Email = email
		}
	}

	return &Session{Subject: verified.Subject, Email: claims.Email}, nil
}

// Authenticate returns the session carried by a request.
func (a *Authenticator) Authenticate(_ context.Context, r *http.Request) (*Session, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, ErrNoSession
	}

	payload, ok := a.verify(cookie.Value)
	if !ok {
		return nil, fmt.Errorf("%w: signature does not match", ErrNoSession)
	}

	var stored struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
		Expires int64  `json:"exp"`
	}
	if err := json.Unmarshal([]byte(payload), &stored); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoSession, err)
	}
	if time.Now().Unix() > stored.Expires {
		return nil, fmt.Errorf("%w: expired", ErrNoSession)
	}

	return &Session{Subject: stored.Subject, Email: stored.Email}, nil
}

// Logout clears the session cookie.
func (a *Authenticator) Logout(w http.ResponseWriter) {
	a.clearCookie(w, sessionCookie)
}

func (a *Authenticator) setSession(w http.ResponseWriter, s *Session) error {
	payload, err := json.Marshal(struct {
		Subject string `json:"sub"`
		Email   string `json:"email"`
		Expires int64  `json:"exp"`
	}{s.Subject, s.Email, time.Now().Add(sessionLifetime).Unix()})
	if err != nil {
		return fmt.Errorf("encode session: %w", err)
	}

	a.setCookie(w, sessionCookie, a.sign(string(payload)), sessionLifetime)
	return nil
}

// sign returns payload with an HMAC appended, so it can be handed to a browser
// and trusted when it comes back.
func (a *Authenticator) sign(payload string) string {
	mac := hmac.New(sha256.New, a.secret)
	mac.Write([]byte(payload))

	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// verify checks a signed value and returns its payload.
func (a *Authenticator) verify(signed string) (string, bool) {
	encoded, signature, ok := strings.Cut(signed, ".")
	if !ok {
		return "", false
	}

	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	got, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return "", false
	}

	mac := hmac.New(sha256.New, a.secret)
	mac.Write(payload)

	if !hmac.Equal(got, mac.Sum(nil)) {
		return "", false
	}
	return string(payload), true
}

func (a *Authenticator) setCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(ttl),
	})
}

func (a *Authenticator) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   a.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// randomString returns an unguessable value for use as OAuth state.
func randomString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate random value: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// GenerateSecret returns a new hex-encoded session secret.
func GenerateSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ParseSecret decodes a configured session secret.
func ParseSecret(s string) ([]byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		if len(s) >= 32 {
			return []byte(s), nil
		}
		return nil, fmt.Errorf("session secret is not valid base64 and is shorter than 32 characters: %w", err)
	}
	if len(b) < 32 {
		return nil, errors.New("session secret must decode to at least 32 bytes, got " + strconv.Itoa(len(b)))
	}
	return b, nil
}
