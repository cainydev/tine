# tine

api to mcp proxy. each configured integration instance is served at its own
endpoint:

```
POST /<user>/<integration>/<id>
```

## one endpoint per connection

a client loads every tool in `tools/list` into context on every request, and
picks worse from a larger set. ten endpoints of ten tools cost a client only the
ones it mounted.

one endpoint holds one credential. a request handler never has access to another
tenant's secrets, because it never loads them.

tine does not merge integrations behind a shared url, and does not expose
`search_tools` / `execute_tool` indirection. tools are typed and called directly.

## status

early. the request path works: authentication, resolution, per-instance mcp
servers, one integration. there is no admin ui yet.

## running

```sh
go build -o bin/tine ./cmd/tine

# 32-byte key that seals stored credentials. losing it means reauthenticating
# every integration.
./bin/tine genkey

# create a user and an instance
./bin/tine seed -subject <oidc-subject> -user john -integration deutsche-bahn

TINE_PUBLIC_URL=https://tine.example \
TINE_OIDC_ISSUER=https://your-idp.example \
TINE_OIDC_AUDIENCE=tine \
TINE_MASTER_KEY=<from genkey> \
./bin/tine
```

### configuration

| variable | required | default | meaning |
|---|---|---|---|
| `TINE_PUBLIC_URL` | yes | | externally reachable base url, published as the oauth resource identifier |
| `TINE_OIDC_ISSUER` | yes* | | identity provider issuer url |
| `TINE_OIDC_AUDIENCE` | yes* | | audience claim tokens must carry |
| `TINE_MASTER_KEY` | yes | | hex key sealing stored credentials, from `tine genkey` |
| `TINE_ADDR` | no | `:8080` | listen address |
| `TINE_DATABASE_PATH` | no | `tine.db` | sqlite file |
| `TINE_LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |
| `TINE_SHUTDOWN_TIMEOUT` | no | `15s` | grace period for in-flight requests |
| `TINE_DEV_MODE` + `TINE_DEV_SUBJECT` | no | | skip token validation, treat every caller as the given subject. local only, both must be set. |

\* not required with `TINE_DEV_MODE=1`.

## authentication

tine is an oauth 2.1 resource server. it validates bearer tokens against an oidc
provider and issues none itself. `TINE_OIDC_ISSUER` accepts workos, authentik,
keycloak, zitadel, or anything else publishing a discovery document. no provider
specific code.

an unauthenticated request gets `401` with a `WWW-Authenticate` header pointing
at `/.well-known/oauth-protected-resource` (rfc 9728). that is how an mcp client
finds where to authenticate.

a valid token for a different subject gets `404`, not `403`. a token for another
account should not reveal that an endpoint exists.

## integrations

integrations are go packages compiled into the binary, contributed by pull
request. there is no plugin sandbox and no configuration language, so an
integration can call several endpoints concurrently, combine results, or use a
protocol other than http.

an integration implements [`integrations.Integration`](integrations/integration.go):
slug, name, version, instance parameters, and a `Bind` returning the tool set for
one configured instance.

the mcp sdk infers tool schemas from go types, so parameters cannot drift from
the handler.

output schemas must be objects. a handler returning a slice produces a top level
array schema, which clients reject. wrap slices in a struct.

## development

```sh
just check   # fmt, lint, test. run before every commit.
just test    # tests with -race -shuffle=on
just build
```

needs go 1.26+, [just](https://github.com/casey/just),
[golangci-lint](https://golangci-lint.run), and [sqlc](https://sqlc.dev) if you
change sql.

## license

[unlicense](LICENSE). public domain.
