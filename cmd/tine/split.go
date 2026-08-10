package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// splitSession runs an agent above a live request log, using tmux.
type splitSession struct {
	name       string
	logPath    string
	configPath string
	dir        string
}

// newSplitSession creates the file the log pane follows and the tmux
// configuration the session starts from.
func newSplitSession(port string) (*splitSession, error) {
	dir, err := os.MkdirTemp("", "tine-dev-")
	if err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}

	logPath := filepath.Join(dir, "log")
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		return nil, fmt.Errorf("create log file: %w", err)
	}

	s := &splitSession{
		name:       "tine-" + port,
		logPath:    logPath,
		configPath: filepath.Join(dir, "tmux.conf"),
		dir:        dir,
	}

	if err := os.WriteFile(s.configPath, []byte(tmuxConfig()), 0o600); err != nil {
		return nil, fmt.Errorf("write tmux config: %w", err)
	}
	return s, nil
}

// tmuxConfig returns the settings the session starts with.
func tmuxConfig() string {
	lines := []string{
		"set -g status off",
		"set -g pane-border-status off",
		"set -g pane-border-style fg=colour238",
		"set -g pane-active-border-style fg=colour238",

		"set -g escape-time 0",

		"set -g display-time 1000",

		"set -g remain-on-exit off",

		"set -g allow-passthrough on",
	}

	if term := os.Getenv("TERM"); term != "" && os.Getenv("COLORTERM") == "truecolor" {
		lines = append(lines,
			`set -g default-terminal "tmux-direct"`,
			`set -ga terminal-features ",`+term+`:RGB:usstyle:ccolour:cstyle:focus:title:clipboard"`,
		)
	}

	return strings.Join(lines, "\n") + "\n"
}

// Close removes the session directory.
func (s *splitSession) Close() error {
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("remove session directory: %w", err)
	}
	return nil
}

// Start creates a detached tmux session with the agent on top and the log
// below, then returns a writer for the log pane.
func (s *splitSession) Start(ctx context.Context, agent agentCommand, logPercent int) (*os.File, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux is required for a split launch: %w", err)
	}

	_ = exec.CommandContext(ctx, "tmux", "kill-session", "-t", s.name).Run() //nolint:errcheck // absent session is expected

	quoted := make([]string, 0, len(agent.args)+3)
	quoted = append(quoted, "TMUX=", "TMUX_PANE=", shellQuote(agent.path))
	for _, arg := range agent.args {
		quoted = append(quoted, shellQuote(arg))
	}
	wrapped := strings.Join(quoted, " ") + "; tmux kill-session -t " + shellQuote(s.name)

	args := []string{"-f", s.configPath, "new-session", "-d", "-s", s.name, "-n", "tine"}
	if w, h, err := terminalSize(); err == nil {
		args = append(args, "-x", strconv.Itoa(w), "-y", strconv.Itoa(h))
	}
	args = append(args, "--", "sh", "-c", wrapped)

	create := exec.CommandContext(ctx, "tmux", args...) //nolint:gosec // fixed program, generated arguments
	create.Env = append(os.Environ(), "COLORTERM=truecolor")
	if out, err := create.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("create tmux session: %w: %s", err, out)
	}

	logPane := exec.CommandContext(ctx, "tmux",
		"split-window", "-v", "-d",
		"-p", strconv.Itoa(logPercent),
		"-t", s.name+":tine",
		"--", "sh", "-c", "tail -n +1 -f "+shellQuote(s.logPath),
	)
	if out, err := logPane.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("split tmux window: %w: %s", err, out)
	}

	w, err := os.OpenFile(s.logPath, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return w, nil
}

// Attach hands the terminal to the tmux session and blocks until it ends.
func (s *splitSession) Attach(ctx context.Context) error {
	attach := exec.CommandContext(ctx, "tmux", "attach-session", "-t", s.name)
	attach.Env = append(os.Environ(), "COLORTERM=truecolor")
	attach.Stdin = os.Stdin
	attach.Stdout = os.Stdout
	attach.Stderr = os.Stderr

	if err := attach.Run(); err != nil {
		return fmt.Errorf("attach to tmux session: %w", err)
	}
	return nil
}

// Kill ends the tmux session if it is still running.
func (s *splitSession) Kill(ctx context.Context) {
	cmd := exec.CommandContext(ctx, "tmux", "kill-session", "-t", s.name)
	_ = cmd.Run() //nolint:errcheck // best effort teardown
}

// terminalSize reports the size of the controlling terminal.
func terminalSize() (int, int, error) {
	ws, err := unix.IoctlGetWinsize(int(os.Stdout.Fd()), unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, fmt.Errorf("get terminal size: %w", err)
	}
	return int(ws.Col), int(ws.Row), nil
}

// shellQuote renders a string safe to embed in a single-quoted shell word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// agentCommand is a resolved agent invocation.
type agentCommand struct {
	path string
	args []string
}
