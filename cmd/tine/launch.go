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

// launchAgent starts an agent connected only to the given endpoint.
//
// The configuration is passed on the command line rather than written to disk,
// so a dev run leaves nothing behind.
func launchAgent(ctx context.Context, agent, name, url string) error {
	config, err := clientConfig(name, url)
	if err != nil {
		return err
	}

	var cmd *exec.Cmd
	switch agent {
	case "claude":
		// --strict-mcp-config so the session sees this endpoint and nothing
		// else, which is the point of testing one integration in isolation.
		cmd = exec.CommandContext(ctx, "claude", //nolint:gosec // fixed program name, config is generated
			"--strict-mcp-config",
			"--mcp-config", string(config),
		)
	default:
		return fmt.Errorf("unknown agent %q", agent)
	}

	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// The agent exited non-zero, which is its business, not a tine
			// failure.
			return nil
		}
		return fmt.Errorf("launch %s: %w", agent, err)
	}
	return nil
}
