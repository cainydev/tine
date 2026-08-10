# tine

API to MCP proxy. Each configured integration instance is served at its own
endpoint, `/<user>/<integration>/<id>`, rather than one aggregated server
exposing hundreds of tools.

A client loads every tool in `tools/list` into context on every request, and
selects worse from a larger set. One endpoint per connection also means one
credential per endpoint: a request handler never holds a credential belonging to
another tenant.

Owner: @cainydev. Domain: cainy.dev.

## Non-negotiables

- **No aggregation.** Never merge tool surfaces from multiple integrations into
  one endpoint. If a feature needs a merged `tools/list`, it is the wrong feature.
- **No meta-tools.** Do not expose `search_actions` / `execute_action` style
  indirection. Tools are typed, named, and directly callable. A model must see
  the real tool, not a catalogue lookup.
- **No workarounds.** If something needs a hack to work, stop and raise it rather
  than shipping the hack. Prefer deleting a feature over carrying a kludge.

## Go standard

Go 1.26 (Arch, current stable). Track the current stable release; do not pin to
old language versions for compatibility with anything.

- **Comments are for the reader, not the author.** Doc comments on exported
  identifiers, in the standard `// Name ...` form. Beyond that, comment only
  what a competent reader cannot derive from the code: an external constraint
  (Shopware tokens last ten minutes), a spec requirement (MCP output schemas
  must be objects), a non-obvious property of a dependency (SQLite enables
  foreign keys per connection). Never narrate what the code does, never record
  what went wrong while writing it, and never justify a decision to an imagined
  reviewer. If a line seems to need explaining, prefer a clearer name or a
  smaller function.
- **Idiomatic over clever.** Follow Effective Go and the standard library's own
  style. If the stdlib solves it, use the stdlib.
- **No unmaintained dependencies.** Before adding one, check its last release and
  whether it is still active. Prefer zero dependencies, then well-maintained ones.
  Every dependency needs a reason that the stdlib cannot cover.
- **Errors:** wrap with `fmt.Errorf("...: %w", err)`. Sentinel errors via
  `errors.Is`, typed errors via `errors.As`. Never discard an error silently
  `_ =` needs a comment explaining why.
- **Context:** every blocking or IO-bound call takes `ctx context.Context` as its
  first parameter. Honour cancellation; never `context.TODO()` in shipped code.
- **Concurrency:** goroutines must have a clear owner and a clear exit. No
  goroutine without a way to stop it. Guard shared state with the smallest
  possible mutex scope, or avoid sharing.
- **Interfaces** are defined by the consumer, not the producer, and stay small.
- **Logging:** `log/slog`, structured, no `fmt.Println` outside `cmd/`.

### Use the modern stdlib

Verified present in this toolchain, prefer these over third-party equivalents:

- `log/slog` for logging (not logrus/zap)
- `net/http` routing with method+wildcard patterns, `ServeMux` (not gin/chi/echo)
- `testing/synctest` for testing concurrent code with fake time
- `crypto/hkdf` for key derivation
- `iter`, `slices`, `maps` for iteration and collection helpers
- `errors.Join` for multi-error aggregation

`encoding/json/v2` is **not** available here (still GOEXPERIMENT-gated), use
`encoding/json`.

> **Revisit in Go 1.27 (~Feb 2027).** The v2 proposal (golang/go#71497) is closed
> and milestoned Go1.27, where v2 becomes the baseline: `encoding/json` is
> reimplemented on v2's engine by default and the flag inverts to
> `GOEXPERIMENT=nojsonv2`. v1's API and behavior stay, so we get the performance
> win for free with no code change, and v1 will not be deprecated. What is worth
> adopting deliberately then is `encoding/json/jsontext` for the proxy hot path
> forwarding an upstream response into a tool result at the syntax level, without
> round-tripping through `map[string]any`.

## Dependencies

Current and maintained as of 2026-08:

- `github.com/modelcontextprotocol/go-sdk` v1.7.0, official MCP SDK,
  co-maintained with Google, full `2026-07-28` protocol support.
- `github.com/getkin/kin-openapi` v0.146.0, OpenAPI 3 parsing for the
  spec to tool conversion.

Anything else needs justifying against the stdlib first.

## MCP protocol

Target protocol `2026-07-28` (stateless streamable HTTP) as the primary path.

- Stateless is the default: each request carries
  `_meta.io.modelcontextprotocol/{protocolVersion,clientInfo,clientCapabilities}`,
  so any replica can serve any request. Do not introduce per-session server state
  on this path.
- Legacy clients negotiate down to `2025-11-25`; that path is supported but is
  not where new features go.
- `cacheScope` and `ttlMs` on list results must reflect reality. Never emit a
  blanket default, a wrong `cacheScope` on a multi-tenant endpoint is a
  cross-tenant leak, not a stale cache.

## Architecture

```
cmd/tine            entrypoint
internal/gateway    HTTP routing, /<user>/<integration>/<id> to MCP server
internal/credential auth material per instance; Apply + Refresh
internal/openapi    OpenAPI spec to typed MCP tools
internal/store      persistence (instances, credentials, tokens)
internal/config     configuration loading
integrations        predefined integration definitions
```

### Credentials

Credentials are per-instance and encrypted at rest. Envelope encryption with key
rotation designed in from the start, retrofitting it is painful and it is the
part most likely to actually hurt users.

OAuth refresh must be single-flight per credential: two concurrent requests
seeing an expired token must not both refresh, because providers that rotate
refresh tokens on use will invalidate one of them. Persist refreshed tokens back
to the store; an in-memory `oauth2.TokenSource` alone is not sufficient.

## Workflow, run before every commit

```
just check      # fmt + lint + test. The gate. ~1s warm, ~13s cold.
```

Individual steps when iterating:

```
just fmt        # gofumpt + goimports, auto-fixes           (~0.1s)
just lint       # golangci-lint, heavily cached             (~0.6s warm)
just testfast   # tests without -race, for tight loops
just test       # tests with -race -shuffle=on              (the real gate)
just ci         # everything CI runs, uncached
```

**`just check` must pass before any commit.** Do not commit around a failing
lint by adding `//nolint`, fix the cause, or raise it if the rule is wrong.

Speed notes: golangci-lint caches per-package, so repeat runs are sub-second;
only the first run after a dependency change pays full cost. `go test` caches
results per package, so unchanged packages do not re-run. Use `just testfast`
while iterating and `just test` (with `-race`) before committing, the race
detector is worth its cost on a concurrent proxy, but not on every save.

## Integrations

Integrations are Go packages compiled into the binary, contributed by PR. No
sandbox and no configuration language, so an integration may call several
endpoints concurrently, combine results, or use a non-HTTP protocol.

A tool's output schema must be an object. A handler returning a slice produces a
top-level array schema, which clients reject. Wrap slices in a struct.

## Testing

- Table-driven tests, `t.Run` subtests, `t.Parallel()` where safe.
- `testing/synctest` for anything time- or concurrency-dependent.
- No mocking frameworks; hand-write fakes against small interfaces.
- `-race` and `-shuffle=on` are on by default in `just test`; keep them passing.
- `go vet ./...` must stay clean (golangci-lint runs it with `enable-all`).

## Git

Conventional commits, one-line messages. Never commit without being asked.
