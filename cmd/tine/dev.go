package main

import (
	"encoding/json"
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
	endpoint := fmt.Sprintf("/%s/%s/%s", devUser, in.Slug(), devInstanceID)

	gw := gateway.New(
		db,
		gateway.NewIntegrationBuilder(reg, db, db, &http.Client{Timeout: 30 * time.Second}),
		gateway.NewDevAuthenticator(devSubject, publicURL),
		log,
	)

	fmt.Printf("\n  %s\n  %s%s\n\n  auth disabled\n\n", in.Name(), publicURL, endpoint)
	for _, spec := range in.Params() {
		fmt.Printf("  %-12s %s\n", spec.Key, params[spec.Key])
	}
	fmt.Println()

	return serveHTTP(ctx, c.Addr, gw.Handler(), 5*time.Second)
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
