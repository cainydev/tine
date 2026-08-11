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
servers, two integrations. instances and credentials are managed from a web
interface, and clients authenticate with a signed url, a bearer token, or oauth.

## trying it

serves one integration with auth disabled, no database or key needed:

```sh
just dev deutsche-bahn
just dev deutsche-bahn --param language=en
```

point an mcp client at the printed url, or launch one connected to that endpoint
and nothing else:

```sh
./bin/tine dev deutsche-bahn --launch claude
```

the agent gets the top of the terminal and the request log the bottom, so you
see each call as the agent makes it. `--no-split` gives the agent the whole
terminal, `--log-percent` changes the division.

`--print-config` writes the client configuration to stdout instead, for agents
that are not launched directly.

## running

```sh
go build -o bin/tine ./cmd/tine

# 32-byte key sealing stored credentials. losing it means reauthenticating
# every integration.
./bin/tine genkey

# create a user and an instance
./bin/tine seed deutsche-bahn --subject <oidc-subject> --user john

TINE_PUBLIC_URL=https://tine.example \
TINE_OIDC_ISSUER=https://your-idp.example \
TINE_OIDC_AUDIENCE=tine \
TINE_MASTER_KEY=<from genkey> \
./bin/tine serve
```

`tine --help` lists every command, and each command's `--help` lists the
integrations compiled into the binary along with their parameters.

### behind a proxy

tine serves plain http and does not compress. terminate tls and encode at the
proxy:

```
tine.example {
    encode zstd gzip
    reverse_proxy localhost:8080
}
```

compression belongs there rather than in tine: mcp responses stream, and a
handler that buffers to compress would hold a response open until it ended.

a page and the stylesheet are ~5 kb encoded, inside the ~13.5 kb a server sends
before waiting for an acknowledgement, so a cold load costs one round trip. a
test in `internal/web/views` fails if a page grows past that.

### configuration

| variable | required | default | meaning |
|---|---|---|---|
| `TINE_PUBLIC_URL` | yes | | externally reachable base url, published as the oauth resource identifier |
| `TINE_OIDC_ISSUER` | yes* | | identity provider issuer url |
| `TINE_OIDC_AUDIENCE` | yes* | | audience claim tokens must carry |
| `TINE_MASTER_KEY` | yes | | hex key sealing stored credentials and signing urls, from `tine genkey` |
| `TINE_OIDC_CLIENT_ID` | no | | oauth client for the web interface. setting it enables the interface |
| `TINE_OIDC_CLIENT_SECRET` | yes† | | secret for that client |
| `TINE_SESSION_SECRET` | yes† | | signs web session cookies, from `tine secret` |
| `TINE_ADDR` | no | `:8080` | listen address |
| `TINE_DATABASE_PATH` | no | `tine.db` | sqlite file |
| `TINE_LOG_LEVEL` | no | `info` | `debug`, `info`, `warn`, `error` |
| `TINE_SHUTDOWN_TIMEOUT` | no | `15s` | grace period for in-flight requests |
| `TINE_DEV_MODE` + `TINE_DEV_SUBJECT` | no | | skip token validation, treat every caller as the given subject. local only, both must be set. |
| `TINE_ENV_FILE` | no | `.env` | file read at startup. values already in the environment win. |

\* not required with `TINE_DEV_MODE=1`.
† required with `TINE_OIDC_CLIENT_ID`.

## authentication

tine is an oauth 2.1 resource server. it validates provider tokens and issues
none. `TINE_OIDC_ISSUER` accepts workos, authentik, keycloak, zitadel, pocket id,
or anything else publishing a discovery document. no provider specific code.

every endpoint accepts three ways in, so a client presents whichever it has:

- **oauth**, an mcp client running the authorization flow against the issuer.
- **a signed url**, carrying its own proof in `?k=`. nothing is stored, so the
  expiry is the only revocation. `tine connect` mints one.
- **a bearer token**, issued from the web interface for a client that cannot
  open a browser, such as a scheduled job. only its hash is stored, it can be
  scoped to a set of instances, and deleting the row revokes it at once.

an unauthenticated request gets `401` with a `WWW-Authenticate` header pointing
at `/.well-known/oauth-protected-resource` (rfc 9728). that is how an mcp client
finds where to authenticate.

a valid credential for a different subject gets `404`, not `403`. a token for
another account should not reveal that an endpoint exists.

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
