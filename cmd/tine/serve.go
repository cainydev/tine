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

	"github.com/cainydev/tine/internal/config"
	"github.com/cainydev/tine/internal/credential"
	"github.com/cainydev/tine/internal/gateway"
	"github.com/cainydev/tine/internal/store"
)

// serveCmd runs the server as it runs in production, configured entirely from
// the environment.
type serveCmd struct{}

func (*serveCmd) Run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.Level(cfg.LogLevelValue()),
	}))
	slog.SetDefault(log)

	ctx, stop := signalContext()
	defer stop()

	db, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	defer closeStore(db, log)

	sealer, err := credential.NewSealer(cfg.MasterKey)
	if err != nil {
		return fmt.Errorf("master key: %w", err)
	}
	db = db.WithSealer(sealer)
	log.Info("credential sealing ready", slog.String("key_id", sealer.KeyID()))

	var auth gateway.Authenticator
	if cfg.AuthDisabled() {
		log.Warn("token validation is disabled, every caller is treated as the configured dev subject",
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

	reg := registry()
	log.Info("integrations registered", slog.Int("count", len(reg.All())))

	// One client for all upstream calls, so connection pooling and timeouts
	// apply uniformly and an integration cannot opt out of them.
	upstream := &http.Client{Timeout: 30 * time.Second}

	gw := gateway.New(db, gateway.NewIntegrationBuilder(reg, db, db, upstream), auth, log)

	log.Info("listening",
		slog.String("addr", cfg.Addr),
		slog.String("public_url", cfg.PublicURL),
		slog.String("issuer", cfg.OIDCIssuer))

	return serveHTTP(ctx, cfg.Addr, gw.Handler(), cfg.ShutdownTimeout)
}

// genkeyCmd prints a new master key.
type genkeyCmd struct{}

func (*genkeyCmd) Run() error {
	key, err := credential.GenerateMasterKey()
	if err != nil {
		return err
	}
	fmt.Println(key)
	return nil
}

// signalContext returns a context cancelled on SIGINT or SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}

// serveHTTP runs an HTTP server until ctx is cancelled, then drains in-flight
// requests within timeout.
//
// No WriteTimeout is set: MCP responses may stream for a long time, and a write
// deadline would cut them off mid-response.
func serveHTTP(ctx context.Context, addr string, handler http.Handler, timeout time.Duration) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	// Deliberately not derived from ctx: ctx is already cancelled at this point,
	// so inheriting it would cancel the drain immediately and drop in-flight
	// requests instead of letting them finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	//nolint:contextcheck // see above: a derived context would defeat the drain
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
