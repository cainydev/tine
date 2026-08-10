package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// agentCommandFor resolves how to launch an agent against one endpoint.
func agentCommandFor(agent, name, url string) (agentCommand, error) {
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
		return agentCommand{
			path: path,
			args: []string{"--strict-mcp-config", "--mcp-config", string(config)},
		}, nil
	default:
		return agentCommand{}, fmt.Errorf("unknown agent %q", agent)
	}
}

// launchAgent runs an agent in the current terminal until it exits.
func launchAgent(ctx context.Context, agent, name, url string) error {
	resolved, err := agentCommandFor(agent, name, url)
	if err != nil {
		return err
	}

	cmd := exec.CommandContext(ctx, resolved.path, resolved.args...) //nolint:gosec // resolved from a fixed set
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil
		}
		return fmt.Errorf("launch %s: %w", agent, err)
	}
	return nil
}
