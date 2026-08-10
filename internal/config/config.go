// Package config loads tine's runtime configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cainydev/tine/internal/credential"
)

// Config is the fully validated runtime configuration.
type Config struct {
	Addr string

	DatabasePath string

	PublicURL string

	OIDCIssuer string

	OIDCAudience string

	MasterKey string

	OIDCClientID     string
	OIDCClientSecret string

	SessionSecret string

	DevSubject string

	DevMode bool

	LogLevel string

	ShutdownTimeout time.Duration
}

// Load reads configuration from the environment and validates it.
func Load() (*Config, error) {
	envFile := env("TINE_ENV_FILE", DefaultEnvFile)
	if err := loadEnvFile(envFile); err != nil {
		return nil, err
	}

	cfg := &Config{
		Addr:             env("TINE_ADDR", ":8080"),
		DatabasePath:     env("TINE_DATABASE_PATH", "tine.db"),
		PublicURL:        strings.TrimRight(os.Getenv("TINE_PUBLIC_URL"), "/"),
		OIDCIssuer:       strings.TrimRight(os.Getenv("TINE_OIDC_ISSUER"), "/"),
		OIDCAudience:     os.Getenv("TINE_OIDC_AUDIENCE"),
		MasterKey:        os.Getenv("TINE_MASTER_KEY"),
		OIDCClientID:     os.Getenv("TINE_OIDC_CLIENT_ID"),
		OIDCClientSecret: os.Getenv("TINE_OIDC_CLIENT_SECRET"),
		SessionSecret:    os.Getenv("TINE_SESSION_SECRET"),
		DevSubject:       os.Getenv("TINE_DEV_SUBJECT"),
		DevMode:          os.Getenv("TINE_DEV_MODE") == "1",
		LogLevel:         env("TINE_LOG_LEVEL", "info"),
		ShutdownTimeout:  15 * time.Second,
	}

	if raw := os.Getenv("TINE_SHUTDOWN_TIMEOUT"); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return nil, fmt.Errorf("TINE_SHUTDOWN_TIMEOUT %q: %w", raw, err)
		}
		cfg.ShutdownTimeout = d
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	var errs []error

	if c.PublicURL == "" {
		errs = append(errs, errors.New("TINE_PUBLIC_URL is required"))
	} else if err := requireAbsoluteURL("TINE_PUBLIC_URL", c.PublicURL); err != nil {
		errs = append(errs, err)
	}

	if c.DevSubject != "" && !c.DevMode {
		errs = append(errs, errors.New("TINE_DEV_SUBJECT requires TINE_DEV_MODE=1"))
	}

	if !c.AuthDisabled() {
		if c.OIDCIssuer == "" {
			errs = append(errs, errors.New("TINE_OIDC_ISSUER is required"))
		} else if err := requireAbsoluteURL("TINE_OIDC_ISSUER", c.OIDCIssuer); err != nil {
			errs = append(errs, err)
		}

		if c.OIDCAudience == "" {
			errs = append(errs, errors.New("TINE_OIDC_AUDIENCE is required"))
		}
	}

	if err := credential.ValidateMasterKey(c.MasterKey); err != nil {
		errs = append(errs, fmt.Errorf("TINE_MASTER_KEY: %w", err))
	}

	if c.WebEnabled() {
		if c.OIDCClientSecret == "" {
			errs = append(errs, errors.New("TINE_OIDC_CLIENT_SECRET is required with TINE_OIDC_CLIENT_ID"))
		}
		if len(c.SessionSecret) < 32 {
			errs = append(errs, errors.New("TINE_SESSION_SECRET must be at least 32 characters, generate one with `tine secret`"))
		}
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("TINE_LOG_LEVEL %q: want debug, info, warn or error", c.LogLevel))
	}

	if c.ShutdownTimeout <= 0 {
		errs = append(errs, errors.New("TINE_SHUTDOWN_TIMEOUT must be positive"))
	}

	return errors.Join(errs...)
}

// requireAbsoluteURL rejects anything that is not an absolute http(s) URL.
//
// A relative or scheme-less value would produce a protected resource
// identifier that clients cannot dial, and the failure would surface as a
// confusing OAuth error rather than a startup error.
func requireAbsoluteURL(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s %q: %w", name, raw, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%s %q: must be an absolute http or https URL", name, raw)
	}
	if u.Host == "" {
		return fmt.Errorf("%s %q: missing host", name, raw)
	}
	return nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// WebEnabled reports whether the management interface is configured.
func (c *Config) WebEnabled() bool {
	return c.OIDCClientID != ""
}

// RedirectURL is where the identity provider returns a browser after sign in.
func (c *Config) RedirectURL() string {
	return c.PublicURL + "/callback"
}

// SecureCookies reports whether cookies should be marked Secure, which they
// must be over https and cannot be over plain http.
func (c *Config) SecureCookies() bool {
	return strings.HasPrefix(c.PublicURL, "https://")
}

// AuthDisabled reports whether token validation is bypassed.
func (c *Config) AuthDisabled() bool {
	return c.DevMode && c.DevSubject != ""
}

// LogLevelValue maps the configured level onto slog's numeric levels.
func (c *Config) LogLevelValue() int {
	switch c.LogLevel {
	case "debug":
		return -4
	case "warn":
		return 4
	case "error":
		return 8
	default:
		return 0
	}
}

// String redacts the master key so a Config can be logged safely.
func (c *Config) String() string {
	return "Config{" + strings.Join([]string{
		"Addr=" + c.Addr,
		"DatabasePath=" + c.DatabasePath,
		"PublicURL=" + c.PublicURL,
		"OIDCIssuer=" + c.OIDCIssuer,
		"OIDCAudience=" + c.OIDCAudience,
		"MasterKey=<redacted>",
		"LogLevel=" + c.LogLevel,
		"ShutdownTimeout=" + strconv.FormatInt(int64(c.ShutdownTimeout/time.Second), 10) + "s",
	}, " ") + "}"
}
