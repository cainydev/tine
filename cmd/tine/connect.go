package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/cainydev/tine/internal/config"
)

// connectCmd launches an agent against an instance served by a running tine.
type connectCmd struct {
	Instance string `arg:"" help:"Instance path, as user/integration/id, or a full endpoint URL."`
	Launch   string `short:"l" enum:"claude,none" default:"claude" help:"Agent to launch. With none, the client configuration is printed instead."`
	BaseURL  string `short:"b" help:"Base URL of the running server. Defaults to TINE_PUBLIC_URL."`

	ClientID     string `help:"OAuth client the agent registers as. Defaults to TINE_AGENT_CLIENT_ID."`
	ClientSecret string `help:"Secret for that client. Defaults to TINE_AGENT_CLIENT_SECRET."`
	CallbackPort int    `default:"0" help:"Fixed port for the agent's OAuth callback, when the client has a pre-registered redirect URI."`
}

func (c *connectCmd) Run() error {
	url, name, err := c.endpoint()
	if err != nil {
		return err
	}

	if c.Launch == "none" {
		cfg, cfgErr := clientConfig(name, url)
		if cfgErr != nil {
			return cfgErr
		}
		fmt.Println(string(cfg))
		return nil
	}

	ctx, stop := signalContext()
	defer stop()

	fmt.Printf("\n  %s\n\n", url)
	return launchAgent(ctx, c.Launch, name, url, agentAuth{
		ClientID:     firstNonEmpty(c.ClientID, os.Getenv("TINE_AGENT_CLIENT_ID")),
		ClientSecret: firstNonEmpty(c.ClientSecret, os.Getenv("TINE_AGENT_CLIENT_SECRET")),
		CallbackPort: c.CallbackPort,
	})
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
