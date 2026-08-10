// Command tine serves per-instance MCP endpoints for configured API
// integrations.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cainydev/tine/integrations"
	dbint "github.com/cainydev/tine/integrations/db"
	"github.com/cainydev/tine/internal/config"
	"github.com/cainydev/tine/internal/credential"
	"github.com/cainydev/tine/internal/gateway"
	"github.com/cainydev/tine/internal/store"
)

func main() {
	// Subcommands that must work without a full configuration.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "genkey":
			if err := genkey(); err != nil {
				fmt.Fprintf(os.Stderr, "tine: %v\n", err)
				os.Exit(1)
			}
			return
		case "seed":
			if err := seed(os.Args[2:]); err != nil {
				fmt.Fprintf(os.Stderr, "tine: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "tine: %v\n", err)
		os.Exit(1)
	}
}

// genkey prints a fresh master key for TINE_MASTER_KEY.
func genkey() error {
	key, err := credential.GenerateMasterKey()
	if err != nil {
		return err
	}
	fmt.Println(key)
	return nil
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.Level(cfg.LogLevelValue()),
	}))
	slog.SetDefault(log)

	// Cancelled on SIGINT/SIGTERM, which starts graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			log.Error("close store", slog.Any("error", closeErr))
		}
	}()

	var auth gateway.Authenticator
	if cfg.AuthDisabled() {
		log.Warn("token validation is disabled; every caller is treated as the configured dev subject",
			slog.String("subject", cfg.DevSubject))
		auth = gateway.NewDevAuthenticator(cfg.DevSubject, cfg.PublicURL)
	} else {
		// Discovery reaches the identity provider, so a wrong issuer fails here
		// rather than on the first request.
		auth, err = gateway.NewOIDCAuthenticator(ctx, cfg.OIDCIssuer, cfg.OIDCAudience, cfg.PublicURL)
		if err != nil {
			return fmt.Errorf("oidc: %w", err)
		}
	}

	sealer, err := credential.NewSealer(cfg.MasterKey)
	if err != nil {
		return fmt.Errorf("master key: %w", err)
	}
	log.Info("credential sealing ready", slog.String("key_id", sealer.KeyID()))
	db = db.WithSealer(sealer)

	registry := integrations.NewRegistry()
	if err := registry.Register(dbint.New()); err != nil {
		return fmt.Errorf("register integrations: %w", err)
	}
	log.Info("integrations registered", slog.Int("count", len(registry.All())))

	// One client for all upstream calls: connection pooling and timeouts apply
	// uniformly, and an integration cannot opt out of them.
	upstream := &http.Client{Timeout: 30 * time.Second}

	builder := gateway.NewIntegrationBuilder(registry, db, db, upstream)
	gw := gateway.New(db, builder, auth, log)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           gw.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// No WriteTimeout: MCP responses may stream for a long time, and a
		// write deadline would cut them off mid-response.
		IdleTimeout: 120 * time.Second,
		BaseContext: func(_ net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening",
			slog.String("addr", cfg.Addr),
			slog.String("public_url", cfg.PublicURL),
			slog.String("issuer", cfg.OIDCIssuer))
		if serveErr := srv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
	}()

	select {
	case serveErr := <-errCh:
		return fmt.Errorf("serve: %w", serveErr)
	case <-ctx.Done():
		log.Info("shutting down", slog.Duration("timeout", cfg.ShutdownTimeout))
	}

	// A fresh context: ctx is already cancelled, and shutdown needs its own
	// deadline to let in-flight requests finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()

	if shutdownErr := srv.Shutdown(shutdownCtx); shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	log.Info("stopped")
	return nil
}
