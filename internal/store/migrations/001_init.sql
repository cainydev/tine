-- +goose Up

-- A tine account, identified by the subject claim of its OIDC token.
--
-- tine is a resource server: it never issues tokens and stores no passwords.
-- `subject` is the join between an incoming token and everything a user owns.
CREATE TABLE users (
    id         TEXT PRIMARY KEY,
    subject    TEXT NOT NULL UNIQUE,
    -- Appears in the endpoint path. Mutable, so it is never a foreign key.
    slug       TEXT NOT NULL UNIQUE,
    email      TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
) STRICT;

-- An integration definition shipped with tine. The catalogue lives in code;
-- this table records which version an instance was created against so a
-- definition change cannot silently alter a live endpoint.
CREATE TABLE integrations (
    id         TEXT PRIMARY KEY,
    slug       TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    version    TEXT NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

-- One configured, credential-scoped integration: the unit that owns an MCP
-- endpoint at /<user slug>/<integration slug>/<id>.
--
-- `id` is the public path segment. It is minted once at creation and never
-- derived from configuration, so editing an instance never breaks a client that
-- already has the URL.
CREATE TABLE instances (
    id             TEXT PRIMARY KEY,
    user_id        TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    integration_id TEXT NOT NULL REFERENCES integrations(id) ON DELETE RESTRICT,

    display_name   TEXT NOT NULL,

    -- Instance-level parameters (scope, language, region, base URL overrides).
    -- JSON because the shape is defined by the integration, not by tine.
    params         TEXT NOT NULL DEFAULT '{}',

    enabled        INTEGER NOT NULL DEFAULT 1,
    created_at     INTEGER NOT NULL,
    updated_at     INTEGER NOT NULL
) STRICT;

-- Resolution reads (user slug, integration slug, id); the id alone is the
-- primary key, and this index serves listing a user's instances.
CREATE INDEX idx_instances_user ON instances(user_id);

-- Upstream auth material for one instance (Surface B). Separate from instances
-- so credentials can be re-keyed and audited without touching instance rows.
--
-- Secrets are envelope-encrypted: `ciphertext` is sealed with a per-record data
-- key, itself sealed with the master key named by `key_id`. Rotation rewrites
-- rows with a new key_id; nothing here is ever stored in plaintext.
CREATE TABLE credentials (
    id          TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL UNIQUE REFERENCES instances(id) ON DELETE CASCADE,

    -- credential.Kind: none | bearer | header | basic | oauth2
    kind        TEXT NOT NULL,

    ciphertext  BLOB NOT NULL,
    nonce       BLOB NOT NULL,
    key_id      TEXT NOT NULL,

    -- OAuth only. A column rather than part of the sealed blob so the refresh
    -- path can find expiring credentials without decrypting every row.
    expires_at  INTEGER,

    -- Set when upstream rejected the credential and refresh could not fix it.
    -- The endpoint stays routable but fails fast with a reauth hint.
    needs_reauth INTEGER NOT NULL DEFAULT 0,

    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_credentials_expiry ON credentials(expires_at)
    WHERE expires_at IS NOT NULL;

-- +goose Down
DROP TABLE credentials;
DROP TABLE instances;
DROP TABLE integrations;
DROP TABLE users;
