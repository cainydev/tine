-- name: ResolveInstance :one
-- The routing hot path. The id is the primary key, but the user and
-- integration slugs must also match: a correct id under the wrong path does not
-- resolve, so the URL as a whole identifies the instance.
SELECT
    i.id,
    i.display_name,
    i.params,
    u.subject   AS owner_subject,
    u.slug      AS user_slug,
    g.slug      AS integration_slug,
    g.version   AS integration_version
FROM instances i
JOIN users u        ON u.id = i.user_id
JOIN integrations g ON g.id = i.integration_id
WHERE i.id = ?
  AND u.slug = ?
  AND g.slug = ?
  AND i.enabled = 1;

-- name: ListInstancesBySubject :many
SELECT
    i.id,
    i.display_name,
    i.params,
    i.enabled,
    g.slug AS integration_slug
FROM instances i
JOIN users u        ON u.id = i.user_id
JOIN integrations g ON g.id = i.integration_id
WHERE u.subject = ?
ORDER BY i.created_at;

-- name: CreateInstance :one
INSERT INTO instances (
    id, user_id, integration_id, display_name, params, enabled, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, 1, ?, ?)
RETURNING *;

-- name: UpdateInstanceParams :exec
UPDATE instances SET params = ?, updated_at = ? WHERE id = ?;

-- name: SetInstanceEnabled :exec
UPDATE instances SET enabled = ?, updated_at = ? WHERE id = ?;

-- name: DeleteInstance :exec
DELETE FROM instances WHERE id = ?;

-- name: GetUserBySubject :one
SELECT * FROM users WHERE subject = ?;

-- name: CreateUser :one
INSERT INTO users (id, subject, slug, email, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetIntegrationBySlug :one
SELECT * FROM integrations WHERE slug = ?;

-- name: UpsertIntegration :one
INSERT INTO integrations (id, slug, name, version, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (slug) DO UPDATE SET
    name    = excluded.name,
    version = excluded.version
RETURNING *;

-- name: GetInstanceParams :one
SELECT params FROM instances WHERE id = ?;
