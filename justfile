# tine, task runner. `just` with no args lists everything.

set positional-arguments

default:
    @just --list

# Fast pre-commit gate: format, lint, test. Run this before every commit.
check: fmt lint test

# Format and fix what can be fixed automatically.
fmt:
    golangci-lint fmt ./...

# Lint. Cached by golangci-lint, so repeat runs are near-instant.
lint:
    golangci-lint run ./...

# Unit tests. Parallel across packages and within them.
test:
    go test -race -shuffle=on ./...

# Tests without -race: much faster, for tight edit loops.
testfast:
    go test -shuffle=on ./...

# Only what changed since last run. Force a clean run with: just retest
retest:
    go clean -testcache && go test -race ./...

# Coverage report opened in the browser.
cover:
    go test -coverprofile=/tmp/tine-cover.out ./...
    go tool cover -html=/tmp/tine-cover.out

tailwind_version := "4.3.3"
tailwind := ".tools/tailwindcss"

# Regenerate templates and the stylesheet.
#
# Tailwind emits only the classes it finds in the templates, so a new class in a
# .templ file has no rule behind it until this runs.
generate: (_tailwind)
    templ generate
    {{ tailwind }} -i internal/web/static/app.src.css -o internal/web/static/app.css --minify

# Fetch the standalone tailwind binary, which bundles its own runtime so the
# stylesheet builds without a node toolchain.
_tailwind:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ -x "{{ tailwind }}" ] && "{{ tailwind }}" --help 2>&1 | grep -q "v{{ tailwind_version }}"; then
        exit 0
    fi
    mkdir -p .tools
    case "$(uname -s)" in
        Linux)  os=linux ;;
        Darwin) os=macos ;;
        *) echo "unsupported system $(uname -s)" >&2; exit 1 ;;
    esac
    case "$(uname -m)" in
        x86_64)  arch=x64 ;;
        aarch64|arm64) arch=arm64 ;;
        *) echo "unsupported architecture $(uname -m)" >&2; exit 1 ;;
    esac
    url="https://github.com/tailwindlabs/tailwindcss/releases/download/v{{ tailwind_version }}/tailwindcss-${os}-${arch}"
    echo "fetching tailwindcss v{{ tailwind_version }}"
    curl -fsSL "$url" -o "{{ tailwind }}"
    chmod +x "{{ tailwind }}"

# Build the binary.
build: generate
    go build -o bin/tine ./cmd/tine

# Tidy modules and verify nothing is stale.
tidy:
    go mod tidy
    go mod verify

# Everything CI would run. Slower, use `just check` day to day.
ci: fmt lint tidy
    go test -race -shuffle=on -count=1 ./...
    go build ./...

# Print a new master key for TINE_MASTER_KEY.
genkey: build
    @./bin/tine genkey

# Print a new session secret for TINE_SESSION_SECRET.
secret: build
    @./bin/tine secret

# Print a .env template with fresh secrets, ready to fill in.
env: build
    @./bin/tine env

# Launch claude against an instance on a running server.
#
#   just connect john/deutsche-bahn/edc1e8b0b00b7e55
#   just connect john/deutsche-bahn/edc1e8b0b00b7e55 --auth=oauth
#
connect instance *args: build
    ./bin/tine connect "$@"

# Serve one integration locally with auth disabled.
#
#   just dev deutsche-bahn
#   just dev deutsche-bahn --param language=en
#
# Run `./bin/tine dev` with no arguments to list integrations.
dev *args: build
    ./bin/tine dev "$@"

# Run the server as it runs in production, from the current environment.
run *args: build
    ./bin/tine "$@"
