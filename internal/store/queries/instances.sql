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

-- name: ListInstancesForUser :many
-- Every instance a user owns, with the integration it was created against.
SELECT
    i.id,
    i.display_name,
    i.params,
    i.enabled,
    i.created_at,
    g.slug    AS integration_slug,
    g.name    AS integration_name,
    g.version AS integration_version,
    c.kind         AS credential_kind,
    c.needs_reauth AS credential_needs_reauth
FROM instances i
JOIN users u        ON u.id = i.user_id
JOIN integrations g ON g.id = i.integration_id
LEFT JOIN credentials c ON c.instance_id = i.id
WHERE u.subject = ?
ORDER BY g.slug, i.created_at;

-- name: ListInstancesForUserIntegration :many
SELECT
    i.id,
    i.display_name,
    i.params,
    i.enabled,
    i.created_at,
    c.kind         AS credential_kind,
    c.needs_reauth AS credential_needs_reauth
FROM instances i
JOIN users u        ON u.id = i.user_id
JOIN integrations g ON g.id = i.integration_id
LEFT JOIN credentials c ON c.instance_id = i.id
WHERE u.subject = ? AND g.slug = ?
ORDER BY i.created_at;

-- name: GetInstanceForUser :one
-- Ownership is part of the lookup, so a handler cannot forget to check it.
SELECT
    i.id,
    i.user_id,
    i.display_name,
    i.params,
    i.enabled,
    g.slug    AS integration_slug,
    g.name    AS integration_name,
    g.version AS integration_version,
    c.kind         AS credential_kind,
    c.needs_reauth AS credential_needs_reauth
FROM instances i
JOIN users u        ON u.id = i.user_id
JOIN integrations g ON g.id = i.integration_id
LEFT JOIN credentials c ON c.instance_id = i.id
WHERE u.subject = ? AND i.id = ?;

-- name: DeleteInstanceForUser :execrows
DELETE FROM instances
WHERE instances.id = ?
  AND instances.user_id = (SELECT u.id FROM users u WHERE u.subject = ?);

-- name: UpsertUser :one
INSERT INTO users (id, subject, slug, email, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (subject) DO UPDATE SET
    email      = excluded.email,
    updated_at = excluded.updated_at
RETURNING *;

-- name: DeleteCredential :exec
DELETE FROM credentials WHERE instance_id = ?;
