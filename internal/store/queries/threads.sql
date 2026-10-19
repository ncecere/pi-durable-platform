-- name: ListAgentThreads :many
-- Every thread of the agent, private ones too (platform admins).
SELECT * FROM agent_threads WHERE agent_id = $1 ORDER BY updated_at DESC, thread_id;

-- name: ListVisibleAgentThreads :many
-- The agent's shared threads and the viewer's own private ones (viewer_user
-- for a person, viewer_service for a service account). A reserved thread (no
-- message yet) is listed for its creator only.
SELECT * FROM agent_threads
WHERE agent_id = sqlc.arg(agent_id)
  AND ((visibility = 'shared' AND NOT reserved)
       OR (sqlc.narg(viewer_user)::uuid IS NOT NULL AND created_by_user = sqlc.narg(viewer_user)::uuid)
       OR (sqlc.narg(viewer_service)::uuid IS NOT NULL AND created_by_service = sqlc.narg(viewer_service)::uuid))
ORDER BY updated_at DESC, thread_id;

-- name: ClaimAgentThread :one
-- The record of a thread a first message is about to be relayed to, written
-- before relaying (its creator and visibility), or of a thread started
-- without a message (reserved). Nothing when it exists.
INSERT INTO agent_threads (agent_id, thread_id, title, created_by_user, created_by_service, created_at, updated_at, busy, status_at, visibility, reserved)
VALUES (sqlc.arg(agent_id), sqlc.arg(thread_id), sqlc.arg(title), sqlc.narg(created_by_user), sqlc.narg(created_by_service),
        sqlc.arg(at), sqlc.arg(at), false, sqlc.arg(at), sqlc.arg(visibility), sqlc.arg(reserved))
ON CONFLICT (agent_id, thread_id) DO NOTHING
RETURNING *;

-- name: UnclaimAgentThread :execrows
-- Removes a claimed record when its first message was refused, unless the
-- runtime has reported the thread since.
DELETE FROM agent_threads t
WHERE t.agent_id = sqlc.arg(agent_id) AND t.thread_id = sqlc.arg(thread_id)
  AND t.created_at = sqlc.arg(at) AND t.status_at = sqlc.arg(at) AND t.last_reply_at IS NULL
  AND NOT EXISTS (SELECT 1 FROM agent_thread_participants p WHERE p.agent_id = t.agent_id AND p.thread_id = t.thread_id);

-- name: ThreadParticipantUser :exec
INSERT INTO agent_thread_participants (agent_id, thread_id, user_id, first_sent_at, last_sent_at)
VALUES (sqlc.arg(agent_id), sqlc.arg(thread_id), sqlc.arg(user_id), sqlc.arg(at), sqlc.arg(at))
ON CONFLICT (agent_id, thread_id, user_id) WHERE user_id IS NOT NULL
DO UPDATE SET last_sent_at = greatest(agent_thread_participants.last_sent_at, EXCLUDED.last_sent_at);

-- name: ThreadParticipantService :exec
INSERT INTO agent_thread_participants (agent_id, thread_id, service_account_id, first_sent_at, last_sent_at)
VALUES (sqlc.arg(agent_id), sqlc.arg(thread_id), sqlc.arg(service_account_id), sqlc.arg(at), sqlc.arg(at))
ON CONFLICT (agent_id, thread_id, service_account_id) WHERE service_account_id IS NOT NULL
DO UPDATE SET last_sent_at = greatest(agent_thread_participants.last_sent_at, EXCLUDED.last_sent_at);

-- name: ListThreadParticipants :many
-- Who has sent to the threads, first sender first.
SELECT * FROM agent_thread_participants
WHERE agent_id = sqlc.arg(agent_id) AND thread_id = ANY(sqlc.arg(thread_ids)::text[])
ORDER BY thread_id, first_sent_at, user_id, service_account_id;

-- name: UserNames :many
-- People's names as threads show them (their display name, else email).
SELECT id, COALESCE(NULLIF(display_name, ''), email::text)::text AS name FROM users WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: ServiceAccountNames :many
SELECT id, name FROM service_accounts WHERE id = ANY(sqlc.arg(ids)::uuid[]);

-- name: GetAgentThread :one
SELECT * FROM agent_threads WHERE agent_id = $1 AND thread_id = $2;

-- name: ThreadSent :one
-- A message was relayed to the thread: it exists (titled after its first
-- message), was used now, and is working (unless a newer report says otherwise).
-- The record normally exists (ClaimAgentThread); visibility is for a record
-- that went missing meanwhile, and never changes an existing one. The first
-- message ends a thread's reservation (and titles it, unless a title was
-- chosen).
INSERT INTO agent_threads (agent_id, thread_id, title, created_by_user, created_by_service, created_at, updated_at, busy, status_at, visibility)
VALUES (sqlc.arg(agent_id), sqlc.arg(thread_id), sqlc.arg(title), sqlc.narg(created_by_user), sqlc.narg(created_by_service),
        sqlc.arg(at), sqlc.arg(at), true, sqlc.arg(at), sqlc.arg(visibility))
ON CONFLICT (agent_id, thread_id) DO UPDATE SET
    title      = CASE WHEN agent_threads.reserved AND agent_threads.title = 'New chat' THEN EXCLUDED.title ELSE agent_threads.title END,
    reserved   = false,
    updated_at = greatest(agent_threads.updated_at, EXCLUDED.updated_at),
    busy       = CASE WHEN EXCLUDED.status_at >= agent_threads.status_at THEN true ELSE agent_threads.busy END,
    last_error = CASE WHEN EXCLUDED.status_at >= agent_threads.status_at THEN NULL ELSE agent_threads.last_error END,
    status_at  = greatest(agent_threads.status_at, EXCLUDED.status_at)
RETURNING *;

-- name: ThreadReported :one
-- The runtime reports a thread's status (and, for a thread the platform has
-- no record of, its title and creation time).
INSERT INTO agent_threads (agent_id, thread_id, title, created_at, updated_at, busy, last_reply_at, last_error, status_at)
VALUES (sqlc.arg(agent_id), sqlc.arg(thread_id), sqlc.arg(title), sqlc.arg(created_at),
        greatest(sqlc.arg(created_at)::timestamptz, coalesce(sqlc.narg(last_reply_at)::timestamptz, sqlc.arg(created_at)::timestamptz)),
        sqlc.arg(busy), sqlc.narg(last_reply_at), sqlc.narg(last_error), sqlc.arg(at))
ON CONFLICT (agent_id, thread_id) DO UPDATE SET
    reserved      = false,
    updated_at    = greatest(agent_threads.updated_at, coalesce(EXCLUDED.last_reply_at, agent_threads.updated_at)),
    busy          = CASE WHEN EXCLUDED.status_at >= agent_threads.status_at THEN EXCLUDED.busy ELSE agent_threads.busy END,
    last_reply_at = CASE WHEN EXCLUDED.status_at >= agent_threads.status_at
                         THEN coalesce(EXCLUDED.last_reply_at, agent_threads.last_reply_at) ELSE agent_threads.last_reply_at END,
    last_error    = CASE WHEN EXCLUDED.status_at >= agent_threads.status_at THEN EXCLUDED.last_error ELSE agent_threads.last_error END,
    status_at     = greatest(agent_threads.status_at, EXCLUDED.status_at)
RETURNING *;

-- name: RenameAgentThread :one
UPDATE agent_threads SET title = $3 WHERE agent_id = $1 AND thread_id = $2 RETURNING *;

-- name: DeleteAgentThread :many
DELETE FROM agent_threads WHERE agent_id = $1 AND thread_id = $2 RETURNING *;

-- name: DeleteAgentThreadsNotIn :execrows
-- Threads the runtime no longer has (deleted while the platform missed it),
-- recorded well before the runtime's report: a thread recorded while the
-- agent was starting (the platform records it before relaying its first
-- message, which waits for the start) may be missing from a list the runtime
-- took a moment earlier, and must not be forgotten (a private thread would
-- come back shared when the runtime next reports it). The next start
-- reconciles those. Reserved threads (no message yet) are not the runtime's
-- to know: DeleteStaleReservedThreads removes the abandoned ones.
DELETE FROM agent_threads
WHERE agent_id = sqlc.arg(agent_id) AND created_at < sqlc.arg(before)::timestamptz - interval '5 minutes' AND NOT reserved
  AND NOT (thread_id = ANY(sqlc.arg(keep)::text[]));

-- name: DeleteStaleReservedThreads :execrows
-- Threads the platform created that nobody has sent to in 30 days (with their
-- MCP servers).
DELETE FROM agent_threads WHERE reserved AND created_at < now() - interval '30 days';

-- name: NotifyThreads :exec
SELECT pg_notify('pdp_threads', sqlc.arg(payload)::text);
