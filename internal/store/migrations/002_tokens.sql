-- +goose Up

-- Long-lived bearer tokens a user issues to reach their own endpoints
-- (Surface A). Unlike a signed URL, a token is revocable: the row is the grant,
-- so deleting it ends access immediately.
--
-- Only the SHA-256 of the token is stored. The token itself is shown once at
-- creation and is unrecoverable afterwards, so a copy of this table does not
-- yield a usable credential.
CREATE TABLE tokens (
    id      TEXT PRIMARY KEY,

    -- Owner. Denormalised from users.subject rather than joined, because
    -- verification happens on the request path and the gateway compares this
    -- against the resolved instance's owner.
    subject TEXT NOT NULL REFERENCES users(subject) ON DELETE CASCADE,

    name    TEXT NOT NULL,

    hash    TEXT NOT NULL UNIQUE,

    -- Whether the token is restricted to the instances in token_grants.
    --
    -- Recorded here rather than inferred from the grant count, because deleting
    -- an instance cascades its grants away: a scoped token whose only instance
    -- is gone must reach nothing, not everything.
    scoped  INTEGER NOT NULL DEFAULT 0,

    -- NULL never expires. A cron job is expected to outlive any fixed date, so
    -- expiry is opt-in rather than a default that fails silently at 3am.
    expires_at   INTEGER,

    created_at   INTEGER NOT NULL,

    -- Written at most once per minute, so a token in constant use does not turn
    -- every read into a write on a single-writer database.
    last_used_at INTEGER
) STRICT;

-- Verification looks a token up by hash on every request, so this index is on
-- the hot path.
CREATE UNIQUE INDEX idx_tokens_hash ON tokens(hash);

CREATE INDEX idx_tokens_subject ON tokens(subject);

-- A token's scope, as a set of instances.
--
-- A token that is not scoped reaches every instance its subject owns, now and
-- in future. A scoped token reaches exactly the instances named here, so a
-- grant can cover a subset without needing a token per endpoint.
CREATE TABLE token_grants (
    token_id    TEXT NOT NULL REFERENCES tokens(id) ON DELETE CASCADE,
    instance_id TEXT NOT NULL REFERENCES instances(id) ON DELETE CASCADE,

    PRIMARY KEY (token_id, instance_id)
) STRICT;

-- Verification asks whether one token covers one instance, on the request path.
CREATE INDEX idx_token_grants_instance ON token_grants(instance_id);

-- +goose Down
DROP TABLE token_grants;
DROP TABLE tokens;
