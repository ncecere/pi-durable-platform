-- OAuth Connect (internal/oauth).

-- name: GetOAuthSettings :one
SELECT * FROM oauth_settings WHERE id = 1;

-- name: LockOAuthSettings :one
SELECT * FROM oauth_settings WHERE id = 1 FOR UPDATE;

-- name: UpdateTrustedClients :one
UPDATE oauth_settings
SET trusted_clients = @trusted_clients, revision = revision + 1, updated_by = @updated_by, updated_at = now()
WHERE id = 1
RETURNING *;

-- name: GetOAuthClient :one
-- A registered client used since unused_since.
SELECT * FROM oauth_clients WHERE client_id = @client_id AND last_used_at > @unused_since;

-- name: InsertOAuthClient :one
INSERT INTO oauth_clients (client_id, client_name, redirect_uris, client_uri, logo_uri)
VALUES (@client_id, @client_name, @redirect_uris, @client_uri, @logo_uri)
RETURNING *;

-- name: TouchOAuthClient :exec
UPDATE oauth_clients SET last_used_at = now()
WHERE client_id = $1 AND last_used_at < now() - interval '1 minute';

-- name: DeleteUnusedOAuthClients :execrows
DELETE FROM oauth_clients WHERE last_used_at <= @unused_since;

-- name: GetActiveOAuthGrant :one
SELECT * FROM oauth_grants
WHERE user_id = @user_id AND client_id = @client_id AND revoked_at IS NULL;

-- name: InsertOAuthGrant :one
INSERT INTO oauth_grants (user_id, client_id, client_kind, client_name, client_uri, logo_uri, scopes, resource)
VALUES (@user_id, @client_id, @client_kind, @client_name, @client_uri, @logo_uri, @scopes, @resource)
RETURNING *;

-- name: LockOAuthGrant :one
SELECT * FROM oauth_grants WHERE id = $1 FOR UPDATE;

-- name: RevokeOAuthGrant :execrows
UPDATE oauth_grants SET revoked_at = now(), revoked_reason = @reason
WHERE id = @id AND revoked_at IS NULL;

-- name: DeleteOAuthGrantTokens :exec
DELETE FROM oauth_tokens WHERE grant_id = $1;

-- name: DeleteOAuthGrantCodes :exec
DELETE FROM oauth_codes WHERE grant_id = $1;

-- name: TouchOAuthGrant :exec
-- Records a use, at most once a minute.
UPDATE oauth_grants SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: ListUserOAuthGrants :many
-- A person's active grants (connected apps), newest first.
SELECT * FROM oauth_grants
WHERE user_id = $1 AND revoked_at IS NULL
ORDER BY created_at DESC, id DESC;

-- name: DeleteExpiredOAuthCodes :execrows
DELETE FROM oauth_codes WHERE expires_at <= now();

-- name: InsertOAuthCode :exec
INSERT INTO oauth_codes (code_hash, grant_id, client_id, redirect_uri, code_challenge, resource, expires_at)
VALUES (@code_hash, @grant_id, @client_id, @redirect_uri, @code_challenge, @resource, @expires_at);

-- name: TakeOAuthCode :one
-- Consumes a code: it is deleted whether or not the exchange succeeds.
DELETE FROM oauth_codes WHERE code_hash = $1 RETURNING *;

-- name: DeleteExpiredOAuthTokens :execrows
DELETE FROM oauth_tokens WHERE expires_at <= now();

-- name: InsertOAuthToken :exec
INSERT INTO oauth_tokens (grant_id, kind, token_hash, pepper_id, resource, expires_at)
VALUES (@grant_id, @kind, @token_hash, @pepper_id, @resource, @expires_at);

-- name: GetOAuthToken :one
-- A token of a kind by its digest, with its grant and person.
SELECT sqlc.embed(t), sqlc.embed(gr), sqlc.embed(u)
FROM oauth_tokens t
JOIN oauth_grants gr ON gr.id = t.grant_id
JOIN users u ON u.id = gr.user_id
WHERE t.token_hash = @token_hash AND t.kind = @kind;

-- name: LockOAuthToken :one
SELECT * FROM oauth_tokens WHERE id = $1 FOR UPDATE;

-- name: RotateOAuthToken :execrows
UPDATE oauth_tokens SET rotated_at = now() WHERE id = $1 AND rotated_at IS NULL;

-- name: DeleteOAuthToken :exec
DELETE FROM oauth_tokens WHERE id = $1;
