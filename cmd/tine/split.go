package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

// splitSession runs an agent above a live request log, using tmux.
//
// tmux gives each pane a real tty, so the agent's full-screen interface behaves
// exactly as it does alone. Composing both regions inside tine would mean
// implementing a terminal emulator, which is far more code than this earns.
type splitSession struct {
	name       string
	logPath    string
	configPath string
	dir        string
}

// newSplitSession creates the fifo the log pane reads and the tmux
// configuration the session starts from.
func newSplitSession(port string) (*splitSession, error) {
	dir, err := os.MkdirTemp("", "tine-dev-")
	if err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}

	logPath := filepath.Join(dir, "log")
	if err := syscall.Mkfifo(logPath, 0o600); err != nil {
		return nil, fmt.Errorf("create log fifo: %w", err)
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
//
// These go through a configuration file rather than set-option calls because
// tmux runs a config when the server first starts, before any pane exists.
// default-terminal in particular decides the TERM a pane inherits, so setting
// it afterwards would come too late for the agent.
func tmuxConfig() string {
	lines := []string{
		// Without a status bar, borders or titles, the two panes read as one
		// terminal split rather than a multiplexer session.
		"set -g status off",
		"set -g pane-border-status off",
		"set -g pane-border-style fg=colour238",
		"set -g pane-active-border-style fg=colour238",

		// Escape belongs to the agent, not to tmux's copy mode.
		"set -g escape-time 0",

		// There is no status bar to show messages on, so keep any that appear
		// brief.
		"set -g display-time 1000",

		// Ending the agent ends its pane rather than leaving a dead one.
		"set -g remain-on-exit off",
	}

	// tmux advertises a 256-colour TERM by default, so an agent reading TERM
	// picks a degraded palette however capable the outer terminal is.
	if term := os.Getenv("TERM"); term != "" && os.Getenv("COLORTERM") == "truecolor" {
		lines = append(lines,
			`set -g default-terminal "tmux-direct"`,
			`set -ga terminal-features ",`+term+`:RGB"`,
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
//
// Opening a fifo for writing blocks until a reader attaches, so the log pane
// must exist first. Start handles that ordering.
func (s *splitSession) Start(ctx context.Context, agent agentCommand, logPercent int) (*os.File, error) {
	if _, err := exec.LookPath("tmux"); err != nil {
		return nil, fmt.Errorf("tmux is required for a split launch: %w", err)
	}

	// A session left behind by an earlier run on this port would block reuse of
	// the name. Its absence is the normal case, so the error is ignored.
	_ = exec.CommandContext(ctx, "tmux", "kill-session", "-t", s.name).Run() //nolint:errcheck // absent session is expected

	// The agent runs wrapped in a shell that ends the session when it exits.
	// Otherwise the log pane, a tail that never ends by itself, would keep the
	// session alive and closing it would take another keypress.
	quoted := make([]string, 0, len(agent.args)+1)
	quoted = append(quoted, shellQuote(agent.path))
	for _, arg := range agent.args {
		quoted = append(quoted, shellQuote(arg))
	}
	wrapped := strings.Join(quoted, " ") + "; tmux kill-session -t " + shellQuote(s.name)

	// A detached session defaults to 80x24, and a split sized against those rows
	// is rescaled on attach, drifting from the requested proportion. Create it
	// at the real terminal size instead.
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
		"--", "sh", "-c", "tail -f "+shellQuote(s.logPath),
	)
	if out, err := logPane.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("split tmux window: %w: %s", err, out)
	}

	// The reader now exists, so this open will not block.
	w, err := os.OpenFile(s.logPath, os.O_WRONLY, 0)
	if err != nil {
		return nil, fmt.Errorf("open log fifo: %w", err)
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
//
// The context is taken explicitly rather than derived from the caller's: by the
// time teardown runs that context is usually already cancelled, and a derived
// one would kill this command before it could clean up.
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
