-- Re-checking IdP group membership after sign-in (internal/auth).

-- name: UpsertIdPToken :exec
-- The refresh token of the person's latest sign-in (sealed), verified now.
INSERT INTO user_idp_tokens (user_id, refresh_token_enc)
VALUES (@user_id, @refresh_token_enc)
ON CONFLICT (user_id) DO UPDATE
SET refresh_token_enc = EXCLUDED.refresh_token_enc, verified_at = now(), checked_at = now();

-- name: DeleteIdPToken :exec
DELETE FROM user_idp_tokens WHERE user_id = @user_id;

-- name: ListDueGroupChecks :many
-- People whose group membership is due for a check (not checked since
-- due_before), of this issuer, not denied already and recently active (a
-- sign-in, a session, or a key or connected app used since active_since).
-- Locked so that replicas don't check the same person twice.
SELECT t.user_id, t.refresh_token_enc, t.verified_at, u.oidc_subject
FROM user_idp_tokens t
JOIN users u ON u.id = t.user_id
WHERE t.checked_at <= @due_before::timestamptz
  AND u.oidc_issuer = @issuer::text
  AND u.group_denied_at IS NULL
  AND (u.last_login_at > @active_since::timestamptz
       OR EXISTS (SELECT 1 FROM sessions s WHERE s.user_id = u.id AND s.expires_at > now())
       OR EXISTS (SELECT 1 FROM api_keys k WHERE k.user_id = u.id AND k.revoked_at IS NULL AND k.last_used_at > @active_since::timestamptz)
       OR EXISTS (SELECT 1 FROM oauth_grants g WHERE g.user_id = u.id AND g.revoked_at IS NULL AND g.last_used_at > @active_since::timestamptz))
ORDER BY t.checked_at
LIMIT @max_rows
FOR UPDATE OF t SKIP LOCKED;

-- name: TouchGroupChecks :exec
UPDATE user_idp_tokens SET checked_at = now() WHERE user_id = ANY(@user_ids::uuid[]);

-- name: LockIdPToken :one
SELECT * FROM user_idp_tokens WHERE user_id = @user_id FOR UPDATE;

-- name: MarkGroupVerified :exec
-- The IdP confirmed the person (and may have rotated the refresh token).
UPDATE user_idp_tokens SET refresh_token_enc = @refresh_token_enc, verified_at = now() WHERE user_id = @user_id;

-- name: SetIdPRefreshToken :exec
-- The IdP rotated the refresh token but could not vouch for the person this
-- time: keep the new token (verified_at stays, so the grace period still counts
-- from the last answer).
UPDATE user_idp_tokens SET refresh_token_enc = @refresh_token_enc WHERE user_id = @user_id;

-- name: SetUserGroups :exec
UPDATE users SET groups = @groups::text[], updated_at = now() WHERE id = @user_id;

-- name: DenyUserGroup :one
-- Nothing when the person is denied already.
UPDATE users
SET group_denied_at = now(), group_denied_reason = @reason, revision = revision + 1, updated_at = now()
WHERE id = @id AND group_denied_at IS NULL
RETURNING *;

-- name: RestoreUserGroup :one
-- Nothing when the person is not denied.
UPDATE users
SET group_denied_at = NULL, group_denied_reason = NULL, revision = revision + 1, updated_at = now()
WHERE id = @id AND group_denied_at IS NOT NULL
RETURNING *;

-- name: RestoreAllGroupDenied :many
-- Everyone denied by the group rule, when the rule is removed.
UPDATE users
SET group_denied_at = NULL, group_denied_reason = NULL, revision = revision + 1, updated_at = now()
WHERE group_denied_at IS NOT NULL
RETURNING id;
