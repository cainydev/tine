-- name: GetCredentialByInstance :one
SELECT * FROM credentials WHERE instance_id = ?;

-- name: UpsertCredential :one
INSERT INTO credentials (
    id, instance_id, kind, ciphertext, nonce, key_id,
    expires_at, needs_reauth, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)
ON CONFLICT (instance_id) DO UPDATE SET
    kind         = excluded.kind,
    ciphertext   = excluded.ciphertext,
    nonce        = excluded.nonce,
    key_id       = excluded.key_id,
    expires_at   = excluded.expires_at,
    needs_reauth = 0,
    updated_at   = excluded.updated_at
RETURNING *;

-- name: MarkCredentialNeedsReauth :exec
UPDATE credentials
SET needs_reauth = 1, updated_at = ?
WHERE id = ?;

-- name: ListCredentialsForRekey :many
-- Rotation: find everything still sealed under an old master key.
SELECT * FROM credentials
WHERE key_id != ?
ORDER BY created_at
LIMIT ?;

-- name: ListExpiringCredentials :many
-- Proactive refresh, so a request does not pay for a token exchange.
SELECT * FROM credentials
WHERE expires_at IS NOT NULL
  AND expires_at < ?
  AND needs_reauth = 0
ORDER BY expires_at
LIMIT ?;
