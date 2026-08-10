package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/cainydev/tine/integrations"
	"github.com/cainydev/tine/internal/store"
)

// seedCmd creates a user and one integration instance, printing the resulting
// endpoint. It exists so a deployment can be exercised before an admin UI does.
type seedCmd struct {
	Integration string            `arg:"" help:"Integration to configure. Compiled in:${integrations}"`
	Subject     string            `required:"" help:"OIDC subject of the owning user."`
	User        string            `required:"" help:"User slug used in the endpoint path."`
	Email       string            `help:"User email."`
	Name        string            `help:"Display name for the instance. Defaults to the integration name."`
	Param       map[string]string `short:"p" placeholder:"KEY=VALUE" help:"Instance parameter. Repeatable."`
	Database    string            `short:"d" default:"tine.db" help:"SQLite database path."`
}

func (c *seedCmd) Run() error {
	in, ok := registry().Get(c.Integration)
	if !ok {
		return fmt.Errorf("unknown integration %q, run `tine seed --help` to list them", c.Integration)
	}

	params, err := integrations.ValidateParams(in, c.Param)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("encode params: %w", err)
	}

	ctx, stop := signalContext()
	defer stop()

	db, err := store.Open(ctx, c.Database)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer closeStore(db, slog.New(slog.NewTextHandler(os.Stderr, nil)))

	displayName := c.Name
	if displayName == "" {
		displayName = in.Name()
	}

	id, err := db.SeedInstance(ctx, store.SeedRequest{
		Subject:            c.Subject,
		UserSlug:           c.User,
		Email:              c.Email,
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

	fmt.Printf("/%s/%s/%s\n", c.User, in.Slug(), id)
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
