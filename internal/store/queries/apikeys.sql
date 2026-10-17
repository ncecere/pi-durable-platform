-- Personal API keys (internal/apikeys).

-- name: CountActiveAPIKeys :one
SELECT count(*)::bigint FROM api_keys WHERE user_id = $1 AND revoked_at IS NULL AND expires_at > now();

-- name: InsertAPIKey :one
INSERT INTO api_keys (user_id, name, prefix, digest, expires_at, pepper_id)
VALUES (@user_id, @name, @prefix, @digest, @expires_at, @pepper_id)
RETURNING *;

-- name: ListAPIKeys :many
-- A person's keys, newest first: active ones, and those revoked or expired
-- within the last 30 days.
SELECT * FROM api_keys
WHERE user_id = $1
  AND LEAST(COALESCE(revoked_at, 'infinity'::timestamptz), expires_at) > now() - interval '30 days'
ORDER BY created_at DESC, id DESC;

-- name: GetUserAPIKey :one
SELECT * FROM api_keys WHERE id = @id AND user_id = @user_id;

-- name: RevokeAPIKey :one
UPDATE api_keys SET revoked_at = now()
WHERE id = @id AND user_id = @user_id AND revoked_at IS NULL
RETURNING *;

-- name: GetAPIKeyByDigest :one
SELECT sqlc.embed(k), sqlc.embed(u)
FROM api_keys k
JOIN users u ON u.id = k.user_id
WHERE k.digest = $1;

-- name: TouchAPIKey :exec
-- Records a use, at most once a minute.
UPDATE api_keys SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: RehashAPIKey :execrows
-- Moves a key matched with the previous pepper to the current one (only if
-- its digest is still the old one).
UPDATE api_keys SET digest = @digest, pepper_id = @pepper_id WHERE id = @id AND digest = @old_digest;

-- name: CountKeysByPepper :one
-- Usable personal and service account keys whose digest is not on the
-- current pepper: on_previous on previous_id, others on neither
-- (`pi-durable-platform doctor`).
WITH usable AS (
    SELECT pepper_id FROM api_keys WHERE revoked_at IS NULL AND expires_at > now()
    UNION ALL
    SELECT pepper_id FROM service_account_keys WHERE revoked_at IS NULL AND expires_at > now()
)
SELECT count(*) FILTER (WHERE pepper_id = @previous_id::bytea)::bigint AS on_previous,
       count(*) FILTER (WHERE pepper_id <> @current_id::bytea AND pepper_id IS DISTINCT FROM @previous_id::bytea)::bigint AS others
FROM usable;
