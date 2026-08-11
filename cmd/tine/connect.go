package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/cainydev/tine/internal/config"
	"github.com/cainydev/tine/internal/gateway"
	"github.com/cainydev/tine/internal/store"
	"github.com/cainydev/tine/internal/web"
)

// Authentication modes an agent can use to reach an endpoint. Every instance
// accepts all of them; this selects which proof the launched agent presents.
const (
	// authURL signs the endpoint URL itself, so the agent needs no credential
	// and runs no authorization flow.
	authURL = "url"

	// authToken issues a bearer token the agent sends in a header.
	authToken = "token"

	// authOAuth leaves the agent to run the OAuth flow against the issuer.
	authOAuth = "oauth"
)

// connectCmd launches an agent against an instance served by a running tine.
type connectCmd struct {
	Instance string `arg:"" help:"Instance path, as user/integration/id, or a full endpoint URL."`
	Launch   string `short:"l" enum:"claude,none" default:"claude" help:"Agent to launch. With none, the client configuration is printed instead."`
	BaseURL  string `short:"b" help:"Base URL of the running server. Defaults to TINE_PUBLIC_URL."`

	Auth string        `enum:"url,token,oauth" default:"url" help:"How the agent authenticates. url signs the endpoint, token issues a bearer token for this session, oauth runs the authorization flow."`
	TTL  time.Duration `default:"1h" help:"How long a signed URL stays valid."`

	Subject string `help:"Subject a signed URL names. Defaults to the instance owner's user segment."`

	ClientID     string `help:"OAuth client the agent registers as. Defaults to TINE_AGENT_CLIENT_ID."`
	ClientSecret string `help:"Secret for that client. Defaults to TINE_AGENT_CLIENT_SECRET."`
	CallbackPort int    `default:"0" help:"Fixed port for the agent's OAuth callback, when the client has a pre-registered redirect URI."`
}

func (c *connectCmd) Run() error {
	endpoint, name, err := c.endpoint()
	if err != nil {
		return err
	}

	var auth agentAuth

	// A token issued here belongs to this session: it is revoked when the agent
	// exits, unless the configuration is printed for something else to use.
	keepToken := false

	switch c.Auth {
	case authURL:
		endpoint, err = c.signEndpoint(endpoint)
		if err != nil {
			return err
		}
	case authToken:
		token, revoke, tokenErr := c.issueToken(endpoint)
		if tokenErr != nil {
			return tokenErr
		}
		defer func() {
			if !keepToken {
				revoke()
			}
		}()

		auth = agentAuth{Headers: map[string]string{"Authorization": "Bearer " + token}}
	case authOAuth:
		auth = agentAuth{
			ClientID:     firstNonEmpty(c.ClientID, os.Getenv("TINE_AGENT_CLIENT_ID")),
			ClientSecret: firstNonEmpty(c.ClientSecret, os.Getenv("TINE_AGENT_CLIENT_SECRET")),
			CallbackPort: c.CallbackPort,
		}
	default:
		return fmt.Errorf("unknown auth mode %q", c.Auth)
	}

	if c.Launch == launchNone {
		cfg, cfgErr := clientConfigWithHeaders(name, endpoint, auth.Headers)
		if cfgErr != nil {
			return cfgErr
		}
		// The printed configuration is meant to outlive this process, so a
		// token in it must survive too.
		keepToken = true
		fmt.Println(string(cfg))
		return nil
	}

	ctx, stop := signalContext()
	defer stop()

	c.announce(endpoint)
	return launchAgent(ctx, c.Launch, name, endpoint, auth)
}

// signEndpoint appends a proof to the endpoint, signed with the server's master
// key.
//
// The key is read from the local environment, so this only works where tine's
// configuration is present. An operator without it has to use --auth=oauth.
func (c *connectCmd) signEndpoint(endpoint string) (string, error) {
	if c.TTL <= 0 {
		return "", fmt.Errorf("--ttl must be positive, got %s", c.TTL)
	}

	cfg, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("signing needs tine's configuration, or use --auth=oauth: %w", err)
	}

	signer, err := gateway.NewSigner(cfg.MasterKey)
	if err != nil {
		return "", fmt.Errorf("url signing key: %w", err)
	}

	path, err := endpointPath(endpoint)
	if err != nil {
		return "", err
	}

	subject, err := c.subjectFor(cfg, path)
	if err != nil {
		return "", err
	}

	return signer.SignedURL(endpoint, path, subject, time.Now().Add(c.TTL)), nil
}

// issueToken creates a bearer token scoped to one instance, and returns it with
// a function that revokes it.
//
// Like signing, this reads tine's configuration locally, so it only works where
// the database is reachable.
func (c *connectCmd) issueToken(endpoint string) (string, func(), error) {
	path, err := endpointPath(endpoint)
	if err != nil {
		return "", nil, err
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 {
		return "", nil, fmt.Errorf("cannot resolve an instance from %q", path)
	}

	cfg, err := config.Load()
	if err != nil {
		return "", nil, fmt.Errorf("issuing a token needs tine's configuration, or use --auth=oauth: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return "", nil, fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "tine: close store: %v\n", closeErr)
		}
	}()

	inst, err := db.Resolve(ctx, parts[0], parts[1], parts[2])
	if err != nil {
		return "", nil, fmt.Errorf("resolve %s: %w", path, err)
	}

	created, err := db.CreateToken(ctx, web.NewToken{
		Subject:     firstNonEmpty(c.Subject, inst.OwnerSubject),
		Name:        "tine connect",
		InstanceIDs: []string{inst.ID},
	})
	if err != nil {
		return "", nil, fmt.Errorf("create token: %w", err)
	}

	revoke := func() {
		revokeCtx, revokeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer revokeCancel()

		revokeDB, openErr := store.Open(revokeCtx, cfg.DatabasePath)
		if openErr != nil {
			fmt.Fprintf(os.Stderr, "tine: revoke token %s: %v\n", created.ID, openErr)
			return
		}
		defer func() {
			if closeErr := revokeDB.Close(); closeErr != nil {
				fmt.Fprintf(os.Stderr, "tine: close store: %v\n", closeErr)
			}
		}()

		if delErr := revokeDB.DeleteToken(revokeCtx, inst.OwnerSubject, created.ID); delErr != nil {
			fmt.Fprintf(os.Stderr, "tine: revoke token %s: %v\n", created.ID, delErr)
		}
	}

	return created.Plaintext, revoke, nil
}

// subjectFor resolves the subject a signed URL names.
//
// The gateway compares it against the instance owner, so it has to be the
// owner's subject as the identity provider spells it, not the user slug in the
// path. Only the store knows that mapping, so without --subject the local
// database is consulted.
func (c *connectCmd) subjectFor(cfg *config.Config, path string) (string, error) {
	if c.Subject != "" {
		return c.Subject, nil
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 3 {
		return "", fmt.Errorf("cannot resolve a subject from %q, pass --subject", path)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return "", fmt.Errorf("resolve subject, or pass --subject: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			fmt.Fprintf(os.Stderr, "tine: close store: %v\n", closeErr)
		}
	}()

	inst, err := db.Resolve(ctx, parts[0], parts[1], parts[2])
	if err != nil {
		return "", fmt.Errorf("resolve %s, or pass --subject: %w", path, err)
	}
	return inst.OwnerSubject, nil
}

// announce prints the endpoint, keeping a signed proof out of the terminal.
func (c *connectCmd) announce(endpoint string) {
	if c.Auth != authURL {
		fmt.Printf("\n  %s\n\n", endpoint)
		return
	}

	base, _, _ := strings.Cut(endpoint, "?")
	fmt.Printf("\n  %s\n  signed url, valid for %s\n\n", base, c.TTL)
}

// endpointPath is the path the signature covers, which is what the gateway sees
// as the request path.
func endpointPath(endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse endpoint %q: %w", endpoint, err)
	}
	return u.Path, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// endpoint resolves the instance argument to a full URL and a server name.
func (c *connectCmd) endpoint() (string, string, error) {
	path := strings.Trim(c.Instance, "/")

	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path, serverName(path), nil
	}

	if strings.Count(path, "/") != 2 {
		return "", "", fmt.Errorf("expected user/integration/id, got %q", c.Instance)
	}

	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		cfg, err := config.Load()
		if err != nil {
			return "", "", fmt.Errorf("no base url: pass --base-url or configure TINE_PUBLIC_URL: %w", err)
		}
		base = cfg.PublicURL
	}

	return base + "/" + path, serverName(path), nil
}

// serverName is the integration segment, which names the server in the agent.
func serverName(path string) string {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 {
		return parts[len(parts)-2]
	}
	return "tine"
}
