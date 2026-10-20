-- OAuth Connect for apps with many people (internal/oauth, internal/agents):
-- app-stable subjects, app secrets, the app-level event stream, and
-- attributing usage to connected apps.

-- name: EnsureOAuthClientSubject :one
-- The person's app-stable pseudonymous ID for a client, created on first use.
INSERT INTO oauth_client_subjects (client_id, user_id) VALUES (@client_id, @user_id)
ON CONFLICT (client_id, user_id) DO UPDATE SET client_id = EXCLUDED.client_id
RETURNING subject;

-- name: UpdateOAuthGrantScopes :one
UPDATE oauth_grants SET scopes = @scopes WHERE id = @id RETURNING *;

-- name: InsertOAuthClientSecret :one
INSERT INTO oauth_client_secrets (client_id, secret_hash, pepper_id, prefix, created_by)
VALUES (@client_id, @secret_hash, @pepper_id, @prefix, @created_by)
RETURNING *;

-- name: ListOAuthClientSecrets :many
SELECT * FROM oauth_client_secrets ORDER BY client_id, created_at, id;

-- name: CountOAuthClientSecrets :one
SELECT count(*)::bigint FROM oauth_client_secrets WHERE client_id = $1;

-- name: DeleteOAuthClientSecret :one
DELETE FROM oauth_client_secrets WHERE id = $1 RETURNING *;

-- name: GetOAuthClientSecret :one
SELECT * FROM oauth_client_secrets WHERE secret_hash = $1;

-- name: TouchOAuthClientSecret :exec
-- Records a use, at most once a minute.
UPDATE oauth_client_secrets SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: ListConnectedOAuthClients :many
-- The apps people have connected now (active grants), most people first, as
-- the newest consent saw them; last_active_at is the latest use (else
-- connection).
SELECT g.client_id,
       (array_agg(g.client_name ORDER BY g.created_at DESC))[1]::text AS client_name,
       (array_agg(g.client_kind ORDER BY g.created_at DESC))[1]::text AS client_kind,
       count(DISTINCT g.user_id)::bigint AS people,
       coalesce(max(g.last_used_at), max(g.created_at))::timestamptz AS last_active_at
FROM oauth_grants g
WHERE g.revoked_at IS NULL
GROUP BY g.client_id
ORDER BY people DESC, g.client_id
LIMIT 500;

-- name: AppStreamPeople :many
-- The people a client's app-level event stream may carry events of for an
-- agent of team_id: an active grant of the client (with a refresh token
-- that hasn't expired, for the platform), an active person (not suspended,
-- not denied by the group re-check), and their role in the agent's team.
SELECT g.id AS grant_id, g.user_id, g.client_name, s.subject, m.role
FROM oauth_grants g
JOIN users u ON u.id = g.user_id
JOIN oauth_client_subjects s ON s.client_id = g.client_id AND s.user_id = g.user_id
JOIN team_members m ON m.team_id = @team_id AND m.user_id = g.user_id
WHERE g.client_id = @client_id AND g.revoked_at IS NULL AND g.resource = @resource
  AND u.status = 'active' AND u.group_denied_at IS NULL
  AND EXISTS (SELECT 1 FROM oauth_tokens t
              WHERE t.grant_id = g.id AND t.kind = 'refresh' AND t.expires_at > now() AND t.resource = @resource)
ORDER BY s.subject;

-- name: AppStreamSubjects :many
-- The subjects of those of user_ids who are a client's people now (as in
-- AppStreamPeople, whatever their team): how the app-level event stream
-- names a thread's creator and participants.
SELECT g.user_id, s.subject
FROM oauth_grants g
JOIN users u ON u.id = g.user_id
JOIN oauth_client_subjects s ON s.client_id = g.client_id AND s.user_id = g.user_id
WHERE g.client_id = @client_id AND g.revoked_at IS NULL AND g.resource = @resource
  AND g.user_id = ANY(@user_ids::uuid[])
  AND u.status = 'active' AND u.group_denied_at IS NULL
  AND EXISTS (SELECT 1 FROM oauth_tokens t
              WHERE t.grant_id = g.id AND t.kind = 'refresh' AND t.expires_at > now() AND t.resource = @resource);

-- name: RecordRunSender :exec
-- Who sent the latest message to a thread, and through which connected app
-- (NULL: not through one).
INSERT INTO agent_run_senders (agent_id, thread_id, grant_id, sent_at)
VALUES (@agent_id, @thread_id, sqlc.narg(grant_id), @sent_at)
ON CONFLICT (agent_id, thread_id) DO UPDATE SET grant_id = EXCLUDED.grant_id, sent_at = EXCLUDED.sent_at;

-- name: RunSenderGrants :many
-- The connected apps behind an agent's work now (optionally one thread's):
-- the latest sender of each thread that is replying, was sent to in the last
-- minute or finished a reply in the last minute. NULL: a sender not through
-- an app. At most two rows: one is an unambiguous answer.
SELECT DISTINCT r.grant_id
FROM agent_run_senders r
JOIN agent_threads t ON t.agent_id = r.agent_id AND t.thread_id = r.thread_id
WHERE r.agent_id = @agent_id
  AND (sqlc.narg(thread_id)::text IS NULL OR r.thread_id = sqlc.narg(thread_id)::text)
  AND (t.busy OR r.sent_at > now() - interval '1 minute' OR t.last_reply_at > now() - interval '1 minute')
LIMIT 2;

-- name: AppUsageByPersonDay :many
-- A client's usage by person (its subject) and UTC day in [since, until):
-- model requests and tokens with a cost estimate at the models' current
-- prices, and tool calls, attributed to grants of the client. Keyset paging
-- on (day, subject).
SELECT x.day, x.subject, x.requests, x.input_tokens, x.output_tokens, x.cache_read_tokens, x.cache_write_tokens,
       x.cost_usd, x.unpriced_requests, x.tool_calls
FROM (
    SELECT coalesce(m.day, c.day)::date AS day, s.subject,
           coalesce(m.requests, 0)::bigint AS requests, coalesce(m.input_tokens, 0)::bigint AS input_tokens,
           coalesce(m.output_tokens, 0)::bigint AS output_tokens, coalesce(m.cache_read_tokens, 0)::bigint AS cache_read_tokens,
           coalesce(m.cache_write_tokens, 0)::bigint AS cache_write_tokens, coalesce(m.cost_usd, 0)::float8 AS cost_usd,
           coalesce(m.unpriced_requests, 0)::bigint AS unpriced_requests, coalesce(c.tool_calls, 0)::bigint AS tool_calls
    FROM (
        SELECT (u.created_at AT TIME ZONE 'UTC')::date AS day, g.user_id,
               sum(u.requests) AS requests, sum(u.input_tokens) AS input_tokens, sum(u.output_tokens) AS output_tokens,
               sum(u.cache_read_tokens) AS cache_read_tokens, sum(u.cache_write_tokens) AS cache_write_tokens,
               sum(coalesce(u.input_tokens * mo.input_price, 0) + coalesce(u.output_tokens * mo.output_price, 0)) / 1e6 AS cost_usd,
               sum(CASE WHEN mo.input_price IS NULL AND mo.output_price IS NULL THEN u.requests ELSE 0 END) AS unpriced_requests
        FROM model_usage u
        JOIN oauth_grants g ON g.id = u.oauth_grant_id
        LEFT JOIN models mo ON mo.id = u.model_id
        WHERE g.client_id = @client_id AND u.created_at >= @since AND u.created_at < @until
        GROUP BY 1, 2
    ) m
    FULL JOIN (
        SELECT (mc.created_at AT TIME ZONE 'UTC')::date AS day, g.user_id, count(*) AS tool_calls
        FROM mcp_calls mc
        JOIN oauth_grants g ON g.id = mc.oauth_grant_id
        WHERE g.client_id = @client_id AND mc.created_at >= @since AND mc.created_at < @until
        GROUP BY 1, 2
    ) c ON c.day = m.day AND c.user_id = m.user_id
    JOIN oauth_client_subjects s ON s.client_id = @client_id AND s.user_id = coalesce(m.user_id, c.user_id)
) x
WHERE sqlc.narg(after_day)::date IS NULL OR (x.day, x.subject) > (sqlc.narg(after_day)::date, sqlc.narg(after_subject)::uuid)
ORDER BY x.day, x.subject
LIMIT @lim;

-- name: GetOAuthGrant :one
SELECT * FROM oauth_grants WHERE id = $1;
