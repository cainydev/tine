package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// mcpConfig is the client configuration format understood by Claude Code,
// Cursor, Zed and others.
type mcpConfig struct {
	Servers map[string]mcpServer `json:"mcpServers"`
}

type mcpServer struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}

// clientConfig returns the configuration a client needs to reach one endpoint.
func clientConfig(name, url string) ([]byte, error) {
	cfg := mcpConfig{Servers: map[string]mcpServer{
		name: {Type: "http", URL: url},
	}}

	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode client config: %w", err)
	}
	return out, nil
}

// scratchDir returns an empty directory for an agent to run in, and a function
// that removes it.
//
// An agent reports its working directory to the model and reads project files
// from it, so launching in place would put whatever happens to be in the current
// directory in front of the model. The name is fixed and generic so the path
// itself carries nothing either.
func scratchDir() (string, func(), error) {
	dir := filepath.Join(os.TempDir(), "mcp-session")

	if err := os.RemoveAll(dir); err != nil {
		return "", nil, fmt.Errorf("clear session directory: %w", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("create session directory: %w", err)
	}

	return dir, func() {
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "tine: remove session directory: %v\n", err)
		}
	}, nil
}

// agentAuth is the OAuth client an agent registers as.
//
// MCP prefers a pre-registered client over dynamic registration, and an
// authorization server that implements neither leaves an agent unable to
// authenticate at all.
type agentAuth struct {
	ClientID     string
	ClientSecret string
	CallbackPort int
}

// agentCommandFor resolves how to launch an agent against one endpoint.
func agentCommandFor(agent, name, url string, auth agentAuth) (agentCommand, error) {
	config, err := clientConfig(name, url)
	if err != nil {
		return agentCommand{}, err
	}

	switch agent {
	case "claude":
		path, err := exec.LookPath("claude")
		if err != nil {
			return agentCommand{}, fmt.Errorf("claude is not on PATH: %w", err)
		}

		args := []string{"--strict-mcp-config", "--mcp-config", string(config)}
		if auth.ClientID != "" {
			args = append(args, "--client-id", auth.ClientID)
		}
		if auth.CallbackPort > 0 {
			args = append(args, "--callback-port", strconv.Itoa(auth.CallbackPort))
		}
		return agentCommand{path: path, args: args, secret: auth.ClientSecret}, nil
	default:
		return agentCommand{}, fmt.Errorf("unknown agent %q", agent)
	}
}

// launchAgent runs an agent in the current terminal until it exits.
func launchAgent(ctx context.Context, agent, name, url string, auth agentAuth) error {
	resolved, err := agentCommandFor(agent, name, url, auth)
	if err != nil {
		return err
	}

	workdir, cleanup, err := scratchDir()
	if err != nil {
		return err
	}
	defer cleanup()

	cmd := exec.CommandContext(ctx, resolved.path, resolved.args...) //nolint:gosec // resolved from a fixed set
	cmd.Dir = workdir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	cmd.Env = os.Environ()
	if resolved.secret != "" {
		cmd.Env = append(cmd.Env, "MCP_CLIENT_SECRET="+resolved.secret)
	}

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil
		}
		return fmt.Errorf("launch %s: %w", agent, err)
	}
	return nil
}
