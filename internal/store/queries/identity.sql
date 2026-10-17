-- name: UpsertLoginUser :one
-- groups are the IdP groups of this sign-in.
INSERT INTO users (oidc_issuer, oidc_subject, email, display_name, claims, groups, last_login_at)
VALUES (@oidc_issuer, @oidc_subject, @email, @display_name, @claims, @groups::text[], now())
ON CONFLICT (oidc_issuer, oidc_subject) DO UPDATE
SET email         = EXCLUDED.email,
    display_name  = EXCLUDED.display_name,
    claims        = EXCLUDED.claims,
    groups        = EXCLUDED.groups,
    last_login_at = now(),
    updated_at    = now()
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = $1;

-- name: InsertBootstrapMarker :execrows
INSERT INTO platform_bootstrap (oidc_issuer, oidc_subject)
VALUES (@oidc_issuer, @oidc_subject)
ON CONFLICT DO NOTHING;

-- name: SetPlatformRole :one
UPDATE users
SET platform_role = @platform_role, revision = revision + 1, updated_at = now()
WHERE id = @id
RETURNING *;

-- name: CreateSession :one
INSERT INTO sessions (user_id, token_digest, csrf_token, user_agent, client_ip, expires_at)
VALUES (@user_id, @token_digest, @csrf_token, @user_agent, @client_ip, @expires_at)
RETURNING id;

-- name: GetSessionUser :one
SELECT sqlc.embed(sessions), sqlc.embed(users)
FROM sessions
JOIN users ON users.id = sessions.user_id
WHERE sessions.token_digest = @token_digest
  AND sessions.expires_at > now();

-- name: DeleteSessionByDigest :exec
DELETE FROM sessions WHERE token_digest = @token_digest;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = @user_id;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= now();

-- name: ListIssuerUsers :many
-- The accounts of one sign-in issuer by subject: the development personas as the dev data has them.
SELECT oidc_subject, email, display_name FROM users WHERE oidc_issuer = @oidc_issuer;

-- name: InsertLoginState :exec
INSERT INTO login_states (state_digest, verifier, nonce, next_path, expires_at)
VALUES (@state_digest, @verifier, @nonce, @next_path, @expires_at);

-- TakeLoginState consumes a pending sign-in: it can be used once.
-- name: TakeLoginState :one
DELETE FROM login_states
WHERE state_digest = @state_digest AND expires_at > now()
RETURNING *;

-- name: DeleteExpiredLoginStates :execrows
DELETE FROM login_states WHERE expires_at <= now();
