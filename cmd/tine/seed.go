package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/cainydev/tine/integrations"
	dbint "github.com/cainydev/tine/integrations/db"
	"github.com/cainydev/tine/internal/store"
)

// seed creates a user and one integration instance, printing the resulting
// endpoint. It exists so a fresh deployment can be exercised before any admin
// UI exists.
func seed(args []string) error {
	fs := flag.NewFlagSet("seed", flag.ContinueOnError)
	var (
		dbPath      = fs.String("db", "tine.db", "SQLite database path")
		subject     = fs.String("subject", "", "OIDC subject of the owning user (required)")
		userSlug    = fs.String("user", "", "user slug used in the endpoint path (required)")
		email       = fs.String("email", "", "user email")
		integration = fs.String("integration", "deutsche-bahn", "integration slug")
		name        = fs.String("name", "", "display name for the instance")
		params      = fs.String("params", "{}", "instance parameters as JSON")
	)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *subject == "" || *userSlug == "" {
		fs.Usage()
		return errors.New("-subject and -user are required")
	}

	registry := integrations.NewRegistry()
	if err := registry.Register(dbint.New()); err != nil {
		return err
	}

	in, ok := registry.Get(*integration)
	if !ok {
		return fmt.Errorf("integration %q is not registered", *integration)
	}

	var rawParams map[string]string
	if err := json.Unmarshal([]byte(*params), &rawParams); err != nil {
		return fmt.Errorf("parse -params: %w", err)
	}
	validated, err := integrations.ValidateParams(in, rawParams)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(validated)
	if err != nil {
		return fmt.Errorf("encode params: %w", err)
	}

	ctx := context.Background()
	st, err := store.Open(ctx, *dbPath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if closeErr := st.Close(); closeErr != nil {
			fmt.Fprintf(fs.Output(), "close store: %v\n", closeErr) //nolint:errcheck // best-effort diagnostic on shutdown
		}
	}()

	displayName := *name
	if displayName == "" {
		displayName = in.Name()
	}

	instanceID, err := st.SeedInstance(ctx, store.SeedRequest{
		Subject:            *subject,
		UserSlug:           *userSlug,
		Email:              *email,
		IntegrationSlug:    in.Slug(),
		IntegrationName:    in.Name(),
		IntegrationVersion: in.Version(),
		DisplayName:        displayName,
		Params:             string(encoded),
		Now:                time.Now().Unix(),
		NewID:              newID,
	})
	if err != nil {
		return err
	}

	fmt.Printf("instance created\n  endpoint: /%s/%s/%s\n", *userSlug, in.Slug(), instanceID)
	return nil
}

// newID returns a short random identifier for a public path segment.
func newID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return hex.EncodeToString(b), nil
}
