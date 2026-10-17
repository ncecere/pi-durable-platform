-- Service accounts and their keys (internal/serviceaccounts).

-- name: ListServiceAccounts :many
-- A team's service accounts by name, with their active keys and creator.
SELECT sqlc.embed(sa),
       (SELECT count(*) FROM service_account_keys k
        WHERE k.service_account_id = sa.id AND k.revoked_at IS NULL AND k.expires_at > now())::bigint AS active_keys,
       u.display_name AS created_by_name
FROM service_accounts sa
LEFT JOIN users u ON u.id = sa.created_by
WHERE sa.team_id = $1
ORDER BY sa.name, sa.id;

-- name: GetServiceAccountView :one
SELECT sqlc.embed(sa),
       (SELECT count(*) FROM service_account_keys k
        WHERE k.service_account_id = sa.id AND k.revoked_at IS NULL AND k.expires_at > now())::bigint AS active_keys,
       u.display_name AS created_by_name
FROM service_accounts sa
LEFT JOIN users u ON u.id = sa.created_by
WHERE sa.id = @id AND sa.team_id = @team_id;

-- name: LockServiceAccount :one
SELECT * FROM service_accounts WHERE id = @id AND team_id = @team_id FOR UPDATE;

-- name: CountServiceAccounts :one
SELECT count(*)::bigint FROM service_accounts WHERE team_id = $1;

-- name: InsertServiceAccount :one
INSERT INTO service_accounts (team_id, name, description, role, enabled, created_by, updated_by)
VALUES (@team_id, @name, @description, @role, @enabled, @created_by, @created_by)
RETURNING *;

-- name: UpdateServiceAccount :one
UPDATE service_accounts
SET name = @name, description = @description, role = @role, enabled = @enabled,
    revision = revision + 1, updated_by = @updated_by, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: DeleteServiceAccount :exec
DELETE FROM service_accounts WHERE id = $1;

-- name: TouchServiceAccount :exec
UPDATE service_accounts SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: CountActiveServiceAccountKeys :one
SELECT count(*)::bigint FROM service_account_keys
WHERE service_account_id = $1 AND revoked_at IS NULL AND expires_at > now();

-- name: InsertServiceAccountKey :one
INSERT INTO service_account_keys (service_account_id, name, prefix, digest, created_by, expires_at, pepper_id)
VALUES (@service_account_id, @name, @prefix, @digest, @created_by, @expires_at, @pepper_id)
RETURNING *;

-- name: ListServiceAccountKeys :many
-- An account's keys, newest first: active ones, and those revoked or
-- expired within the last 30 days.
SELECT sqlc.embed(k), u.display_name AS created_by_name
FROM service_account_keys k
LEFT JOIN users u ON u.id = k.created_by
WHERE k.service_account_id = $1
  AND LEAST(COALESCE(k.revoked_at, 'infinity'::timestamptz), k.expires_at) > now() - interval '30 days'
ORDER BY k.created_at DESC, k.id DESC;

-- name: GetServiceAccountKey :one
SELECT sqlc.embed(k), u.display_name AS created_by_name
FROM service_account_keys k
LEFT JOIN users u ON u.id = k.created_by
WHERE k.id = @id AND k.service_account_id = @service_account_id;

-- name: RevokeServiceAccountKey :one
UPDATE service_account_keys SET revoked_at = now()
WHERE id = @id AND service_account_id = @service_account_id AND revoked_at IS NULL
RETURNING *;

-- name: RevokeServiceAccountKeys :exec
UPDATE service_account_keys SET revoked_at = now() WHERE service_account_id = $1 AND revoked_at IS NULL;

-- name: GetServiceAccountKeyByDigest :one
-- A key by its digest, with its account and the account's team.
SELECT sqlc.embed(k), sqlc.embed(sa), sqlc.embed(t)
FROM service_account_keys k
JOIN service_accounts sa ON sa.id = k.service_account_id
JOIN teams t ON t.id = sa.team_id
WHERE k.digest = $1;

-- name: TouchServiceAccountKey :exec
UPDATE service_account_keys SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: AdminListServiceAccounts :many
-- One page of every team's service accounts ordered by (name, id), with
-- their team, active keys and creator. search (LIKE-escaped by the caller)
-- matches the account's name and its team's slug and name.
SELECT sqlc.embed(sa), t.slug AS team_slug,
       (SELECT count(*) FROM service_account_keys k
        WHERE k.service_account_id = sa.id AND k.revoked_at IS NULL AND k.expires_at > now())::bigint AS active_keys,
       u.display_name AS created_by_name
FROM service_accounts sa
JOIN teams t ON t.id = sa.team_id
LEFT JOIN users u ON u.id = sa.created_by
WHERE (sqlc.narg(team_id)::uuid IS NULL OR sa.team_id = sqlc.narg(team_id)::uuid)
  AND (sqlc.narg(search)::text IS NULL
       OR sa.name ILIKE '%' || sqlc.narg(search)::text || '%' ESCAPE '\'
       OR t.slug ILIKE '%' || sqlc.narg(search)::text || '%' ESCAPE '\'
       OR t.name ILIKE '%' || sqlc.narg(search)::text || '%' ESCAPE '\')
  AND (sqlc.narg(after_name)::text IS NULL OR (sa.name, sa.id) > (sqlc.narg(after_name)::text, sqlc.narg(after_id)::uuid))
ORDER BY sa.name, sa.id
LIMIT @page_size;

-- name: RehashServiceAccountKey :execrows
-- Moves a key matched with the previous pepper to the current one (only if
-- its digest is still the old one).
UPDATE service_account_keys SET digest = @digest, pepper_id = @pepper_id WHERE id = @id AND digest = @old_digest;
