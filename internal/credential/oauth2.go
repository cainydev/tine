package credential

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ClientCredentials authenticates with an OAuth2 client credentials grant.
type ClientCredentials struct {
	TokenURL     string `json:"token_url"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	Scope        string `json:"scope,omitempty"`

	mu        sync.Mutex
	token     string
	expiresAt time.Time

	client *http.Client
}

// Kind identifies this credential as an OAuth2 client credentials grant.
func (*ClientCredentials) Kind() Kind { return KindOAuth2 }

// WithClient returns a copy that uses the given HTTP client for token requests.
func (c *ClientCredentials) WithClient(client *http.Client) *ClientCredentials {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.client = client
	return c
}

// Apply sets the Authorization header, fetching a token if the cached one is
// missing or close to expiry.
func (c *ClientCredentials) Apply(ctx context.Context, req *http.Request) error {
	token, err := c.accessToken(ctx, false)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

// Refresh discards the cached token and fetches a new one.
func (c *ClientCredentials) Refresh(ctx context.Context) error {
	_, err := c.accessToken(ctx, true)
	return err
}

// expiryMargin is how long before stated expiry a token is treated as stale, so
// a request does not travel with a token that expires in flight.
const expiryMargin = 30 * time.Second

func (c *ClientCredentials) accessToken(ctx context.Context, force bool) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force && c.token != "" && time.Now().Before(c.expiresAt.Add(-expiryMargin)) {
		return c.token, nil
	}

	token, expiresIn, err := c.fetch(ctx)
	if err != nil {
		return "", err
	}

	c.token = token
	c.expiresAt = time.Now().Add(expiresIn)
	return token, nil
}

func (c *ClientCredentials) fetch(ctx context.Context) (string, time.Duration, error) {
	if c.TokenURL == "" || c.ClientID == "" || c.ClientSecret == "" {
		return "", 0, errors.New("oauth2 credential is missing a token url, client id or secret")
	}

	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
	}
	if c.Scope != "" {
		form.Set("scope", c.Scope)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.TokenURL,
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := c.client
	if client == nil {
		client = http.DefaultClient
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("request token: %w", err)
	}
	defer func() {
		_ = resp.Body.Close() //nolint:errcheck // body already read
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", 0, fmt.Errorf("read token response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("token endpoint returned %s: %s",
			resp.Status, strings.TrimSpace(string(body)))
	}

	var payload struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", 0, fmt.Errorf("decode token response: %w", err)
	}
	if payload.AccessToken == "" {
		return "", 0, errors.New("token endpoint returned no access token")
	}

	expiresIn := time.Duration(payload.ExpiresIn) * time.Second
	if expiresIn <= 0 {
		expiresIn = 5 * time.Minute
	}
	return payload.AccessToken, expiresIn, nil
}

// MarshalJSON omits the cached token: only the configuration is persisted, so a
// restart fetches a fresh one.
func (c *ClientCredentials) MarshalJSON() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	type stored struct {
		TokenURL     string `json:"token_url"`
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
		Scope        string `json:"scope,omitempty"`
	}

	out, err := json.Marshal(stored{c.TokenURL, c.ClientID, c.ClientSecret, c.Scope})
	if err != nil {
		return nil, fmt.Errorf("marshal oauth2 credential: %w", err)
	}
	return out, nil
}
