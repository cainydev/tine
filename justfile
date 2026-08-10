# tine, task runner. `just` with no args lists everything.

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

# Only what changed since last run (Go's build/test cache does the work).
# Force a clean run with: just retest
retest:
    go clean -testcache && go test -race ./...

# Coverage report opened in the browser.
cover:
    go test -coverprofile=/tmp/tine-cover.out ./...
    go tool cover -html=/tmp/tine-cover.out

# Build the binary.
build:
    go build -o bin/tine ./cmd/tine

# Tidy modules and verify nothing is stale.
tidy:
    go mod tidy
    go mod verify

# Everything CI would run. Slower, use `just check` day to day.
ci: fmt lint tidy
    go test -race -shuffle=on -count=1 ./...
    go build ./...
