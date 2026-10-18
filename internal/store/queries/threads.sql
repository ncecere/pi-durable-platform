-- name: ListAgentThreads :many
SELECT * FROM agent_threads WHERE agent_id = $1 ORDER BY updated_at DESC, thread_id;

-- name: GetAgentThread :one
SELECT * FROM agent_threads WHERE agent_id = $1 AND thread_id = $2;

-- name: ThreadSent :one
-- A message was relayed to the thread: it exists (titled after its first
-- message), was used now, and is working (unless a newer report says otherwise).
INSERT INTO agent_threads (agent_id, thread_id, title, created_by_user, created_by_service, created_at, updated_at, busy, status_at)
VALUES (sqlc.arg(agent_id), sqlc.arg(thread_id), sqlc.arg(title), sqlc.narg(created_by_user), sqlc.narg(created_by_service),
        sqlc.arg(at), sqlc.arg(at), true, sqlc.arg(at))
ON CONFLICT (agent_id, thread_id) DO UPDATE SET
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
    updated_at    = greatest(agent_threads.updated_at, coalesce(EXCLUDED.last_reply_at, agent_threads.updated_at)),
    busy          = CASE WHEN EXCLUDED.status_at >= agent_threads.status_at THEN EXCLUDED.busy ELSE agent_threads.busy END,
    last_reply_at = CASE WHEN EXCLUDED.status_at >= agent_threads.status_at
                         THEN coalesce(EXCLUDED.last_reply_at, agent_threads.last_reply_at) ELSE agent_threads.last_reply_at END,
    last_error    = CASE WHEN EXCLUDED.status_at >= agent_threads.status_at THEN EXCLUDED.last_error ELSE agent_threads.last_error END,
    status_at     = greatest(agent_threads.status_at, EXCLUDED.status_at)
RETURNING *;

-- name: RenameAgentThread :one
UPDATE agent_threads SET title = $3 WHERE agent_id = $1 AND thread_id = $2 RETURNING *;

-- name: DeleteAgentThread :exec
DELETE FROM agent_threads WHERE agent_id = $1 AND thread_id = $2;

-- name: DeleteAgentThreadsNotIn :execrows
-- Threads the runtime no longer has (deleted while the platform missed it),
-- recorded before the runtime's report.
DELETE FROM agent_threads
WHERE agent_id = sqlc.arg(agent_id) AND created_at < sqlc.arg(before) AND NOT (thread_id = ANY(sqlc.arg(keep)::text[]));

-- name: NotifyThreads :exec
SELECT pg_notify('pdp_threads', sqlc.arg(payload)::text);
