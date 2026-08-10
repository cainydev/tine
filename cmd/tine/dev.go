package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cainydev/tine/integrations"
	"github.com/cainydev/tine/internal/gateway"
	"github.com/cainydev/tine/internal/store"
)

const (
	devUser    = "dev"
	devSubject = "dev-user"

	// devInstanceID is fixed so the endpoint survives restarts and a client
	// configured once keeps working.
	devInstanceID = "dev"
)

// devCmd serves one integration locally with authentication disabled.
//
// Everything it needs is created in memory: no database to seed, no master key
// to manage, and a stable endpoint derived from the integration slug.
type devCmd struct {
	Integration string            `arg:"" help:"Integration to serve. Compiled in:${integrations}"`
	Param       map[string]string `short:"p" placeholder:"KEY=VALUE" help:"Instance parameter. Repeatable."`
	Addr        string            `short:"a" default:":8377" help:"Listen address."`
	Verbose     bool              `short:"v" help:"Log at debug level."`
	Launch      string            `short:"l" enum:"none,claude" default:"none" help:"Launch an agent connected to this endpoint and nothing else. One of: none, claude."`
	PrintConfig bool              `help:"Print the MCP client configuration and exit."`
}

func (c *devCmd) Run() error {
	reg := registry()

	in, ok := reg.Get(c.Integration)
	if !ok {
		return fmt.Errorf("unknown integration %q, run `tine dev --help` to list them", c.Integration)
	}

	params, err := integrations.ValidateParams(in, c.Param)
	if err != nil {
		return err
	}

	level := slog.LevelInfo
	if c.Verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ctx, stop := signalContext()
	defer stop()

	// In-memory: dev state should not outlive the process.
	db, err := store.Open(ctx, ":memory:")
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer closeStore(db, log)

	encoded, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode params: %w", err)
	}

	if _, err := db.SeedInstance(ctx, store.SeedRequest{
		Subject:            devSubject,
		UserSlug:           devUser,
		Email:              "dev@localhost",
		IntegrationSlug:    in.Slug(),
		IntegrationName:    in.Name(),
		IntegrationVersion: in.Version(),
		DisplayName:        in.Name(),
		Params:             string(encoded),
		Now:                time.Now().Unix(),
		NewID:              fixedIDs(devInstanceID),
	}); err != nil {
		return fmt.Errorf("create dev instance: %w", err)
	}

	publicURL := "http://localhost" + portOf(c.Addr)
	url := publicURL + fmt.Sprintf("/%s/%s/%s", devUser, in.Slug(), devInstanceID)

	if c.PrintConfig {
		config, err := clientConfig(in.Slug(), url)
		if err != nil {
			return err
		}
		fmt.Println(string(config))
		return nil
	}

	gw := gateway.New(
		db,
		gateway.NewIntegrationBuilder(reg, db, db, &http.Client{Timeout: 30 * time.Second}),
		gateway.NewDevAuthenticator(devSubject, publicURL),
		log,
	)

	fmt.Printf("\n  %s\n  %s\n\n  auth disabled\n\n", in.Name(), url)
	for _, spec := range in.Params() {
		fmt.Printf("  %-12s %s\n", spec.Key, params[spec.Key])
	}
	fmt.Println()

	if c.Launch == "none" {
		return serveHTTP(ctx, c.Addr, gw.Handler(), 5*time.Second)
	}

	// Serve in the background and stop once the agent exits, so one command
	// covers the whole test loop.
	agentCtx, stopServing := context.WithCancel(ctx)
	defer stopServing()

	served := make(chan error, 1)
	go func() { served <- serveHTTP(agentCtx, c.Addr, gw.Handler(), 5*time.Second) }()

	if err := waitForHealth(ctx, publicURL); err != nil {
		return err
	}

	launchErr := launchAgent(ctx, c.Launch, in.Slug(), url)
	stopServing()

	if serveErr := <-served; serveErr != nil {
		return serveErr
	}
	return launchErr
}

// waitForHealth blocks until the server answers, so an agent never starts
// against a socket that is not listening yet.
func waitForHealth(ctx context.Context, baseURL string) error {
	client := &http.Client{Timeout: time.Second}

	for range 50 {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/healthz", nil)
		if err != nil {
			return fmt.Errorf("build health request: %w", err)
		}

		resp, err := client.Do(req)
		if err == nil {
			if closeErr := resp.Body.Close(); closeErr != nil {
				return fmt.Errorf("close health response: %w", closeErr)
			}
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
	return errors.New("server did not become healthy")
}

// fixedIDs returns identifiers that are stable across runs. Dev state is
// recreated on every start, so uniqueness only has to hold within one process.
func fixedIDs(id string) func() (string, error) {
	n := 0
	return func() (string, error) {
		n++
		switch n {
		case 1:
			return id + "-user", nil
		case 2:
			return id + "-integration", nil
		default:
			return id, nil
		}
	}
}

// portOf extracts ":8377" from an address like "127.0.0.1:8377".
func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ":" + addr
}

// closeStore logs a close failure, which is not actionable but is worth seeing.
func closeStore(db *store.Store, log *slog.Logger) {
	if err := db.Close(); err != nil {
		log.Error("close store", slog.Any("error", err))
	}
}
