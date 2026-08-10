package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
)

// DefaultEnvFile is read at startup when present.
const DefaultEnvFile = ".env"

// loadEnvFile applies key=value pairs from a file to the environment.
//
// Values already set in the environment win, so an explicit variable overrides
// the file rather than the other way round. A missing file is not an error:
// configuration through the environment alone is the normal case in production.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() {
		_ = f.Close() //nolint:errcheck // read-only, nothing to report on close
	}()

	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		key, value, err := parseEnvLine(scanner.Text())
		if err != nil {
			return fmt.Errorf("%s line %d: %w", path, line, err)
		}
		if key == "" {
			continue
		}
		if _, set := os.LookupEnv(key); set {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("%s line %d: %w", path, line, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

// parseEnvLine returns the key and value on one line, or an empty key for
// blank lines and comments.
func parseEnvLine(line string) (string, string, error) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", nil
	}

	// `export KEY=value` is common in files meant to be sourced by a shell.
	trimmed = strings.TrimPrefix(trimmed, "export ")

	key, value, ok := strings.Cut(trimmed, "=")
	if !ok {
		return "", "", fmt.Errorf("expected KEY=value, got %q", trimmed)
	}

	key = strings.TrimSpace(key)
	if key == "" {
		return "", "", errors.New("empty key")
	}

	value = strings.TrimSpace(value)
	// Quotes are stripped so a value containing spaces can be written naturally.
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			value = value[1 : len(value)-1]
		}
	}
	return key, value, nil
}
