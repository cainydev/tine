-- name: CreateToken :one
INSERT INTO tokens (
    id, subject, name, hash, scoped, expires_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GrantTokenInstance :exec
INSERT INTO token_grants (token_id, instance_id) VALUES (?, ?);

-- name: GetTokenByHash :one
SELECT * FROM tokens WHERE hash = ?;

-- name: TokenGrantsInstance :one
-- Whether a scoped token names this instance. Unscoped tokens never reach here.
SELECT COUNT(*) FROM token_grants WHERE token_id = ? AND instance_id = ?;

-- name: ListTokensForSubject :many
SELECT * FROM tokens WHERE subject = ? ORDER BY created_at DESC;

-- name: ListGrantsForSubject :many
-- Every grant belonging to a subject's tokens, so a listing can name the
-- endpoints each token reaches without a query per token.
SELECT
    token_grants.token_id,
    token_grants.instance_id,
    instances.display_name AS instance_name,
    integrations.slug      AS integration_slug
FROM token_grants
JOIN tokens       ON tokens.id = token_grants.token_id
JOIN instances    ON instances.id = token_grants.instance_id
JOIN integrations ON integrations.id = instances.integration_id
WHERE tokens.subject = ?;

-- name: DeleteToken :execrows
DELETE FROM tokens WHERE id = ? AND subject = ?;

-- name: TouchToken :exec
-- Skips the write unless a minute has passed, so a token in constant use does
-- not serialise every request behind a write on a single-writer database.
UPDATE tokens
SET last_used_at = ?
WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?);
