-- Tool approvals (docs/approvals.md).

-- name: InsertToolApproval :one
-- A request the runtime reported; nothing when it is already recorded
-- (every rerun of the runtime's hook reports again).
INSERT INTO tool_approvals (agent_id, id, thread_id, tool, call_id, arguments, summary, requested_for_kind,
    requested_for_id, requested_for_name, approver_role, requested_at, expires_at)
VALUES (sqlc.arg(agent_id), sqlc.arg(id), sqlc.arg(thread_id), sqlc.arg(tool), sqlc.arg(call_id), sqlc.arg(arguments),
    sqlc.arg(summary), sqlc.narg(requested_for_kind), sqlc.narg(requested_for_id), sqlc.arg(requested_for_name),
    sqlc.arg(approver_role), sqlc.arg(requested_at), sqlc.arg(expires_at))
ON CONFLICT (agent_id, id) DO NOTHING
RETURNING *;

-- name: GetToolApproval :one
SELECT * FROM tool_approvals WHERE agent_id = $1 AND id = $2;

-- name: GetToolApprovalView :one
SELECT sqlc.embed(t), ag.name AS agent_name, th.visibility AS thread_visibility, th.created_by_user AS thread_user,
    th.created_by_service AS thread_service, th.title AS thread_title
FROM tool_approvals t
JOIN agents ag ON ag.id = t.agent_id
LEFT JOIN agent_threads th ON th.agent_id = t.agent_id AND th.thread_id = t.thread_id
WHERE t.agent_id = $1 AND t.id = $2;

-- name: LockToolApproval :one
SELECT * FROM tool_approvals WHERE agent_id = $1 AND id = $2 FOR UPDATE;

-- name: DecideToolApproval :one
-- A pending request decided (approved or denied); delivered to the runtime next.
UPDATE tool_approvals SET status = sqlc.arg(status), decided_at = now(), decided_by_user = sqlc.narg(decided_by_user),
    decided_by_service = sqlc.narg(decided_by_service), decided_by_name = sqlc.arg(decided_by_name),
    reason = sqlc.arg(reason), next_delivery_at = now()
WHERE agent_id = sqlc.arg(agent_id) AND id = sqlc.arg(id) AND status = 'pending'
RETURNING *;

-- name: CancelToolApproval :one
-- The runtime says the call is gone: nothing to deliver.
UPDATE tool_approvals SET status = 'cancelled', decided_at = now(), delivered_at = now()
WHERE agent_id = $1 AND id = $2 AND status = 'pending'
RETURNING *;

-- name: CancelToolApprovalsNotIn :many
-- At the runtime's start: pending requests it no longer has (asked before
-- its list was taken) are cancelled.
UPDATE tool_approvals SET status = 'cancelled', decided_at = now(), delivered_at = now()
WHERE agent_id = sqlc.arg(agent_id) AND status = 'pending' AND requested_at < sqlc.arg(before)
  AND NOT (id = ANY(sqlc.arg(keep)::text[]))
RETURNING *;

-- name: ExpireToolApprovals :many
-- Pending requests past their expiry are denied (timed_out), at most 100 a
-- pass; each row changes once whichever replica runs it.
UPDATE tool_approvals SET status = 'timed_out', decided_at = now(), next_delivery_at = now()
WHERE (agent_id, id) IN (
    SELECT agent_id, id FROM tool_approvals
    WHERE status = 'pending' AND expires_at <= now()
    ORDER BY expires_at LIMIT 100
    FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: ClaimApprovalDeliveries :many
-- Decisions not yet delivered whose next attempt is due (decided in the last
-- week), each claimed by one replica: its next attempt moves out with a
-- growing delay (30 s, doubling, at most an hour).
UPDATE tool_approvals SET delivery_attempts = delivery_attempts + 1,
    next_delivery_at = now() + make_interval(secs => LEAST(3600, 30 * power(2, LEAST(delivery_attempts, 10))))
WHERE (agent_id, id) IN (
    SELECT agent_id, id FROM tool_approvals
    WHERE status <> 'pending' AND delivered_at IS NULL AND next_delivery_at <= now()
      AND decided_at > now() - interval '7 days'
    ORDER BY next_delivery_at LIMIT 50
    FOR UPDATE SKIP LOCKED)
RETURNING *;

-- name: MarkToolApprovalDelivered :exec
UPDATE tool_approvals SET delivered_at = now() WHERE agent_id = $1 AND id = $2;

-- name: ListAgentToolApprovals :many
-- An agent's requests the viewer may see (in shared threads, and in the
-- viewer's own private ones), newest first: pending, decided (anything else)
-- or all; optionally one thread's.
SELECT sqlc.embed(t), ag.name AS agent_name, th.visibility AS thread_visibility, th.created_by_user AS thread_user,
    th.created_by_service AS thread_service, th.title AS thread_title
FROM tool_approvals t
JOIN agents ag ON ag.id = t.agent_id
LEFT JOIN agent_threads th ON th.agent_id = t.agent_id AND th.thread_id = t.thread_id
WHERE t.agent_id = sqlc.arg(agent_id)
  AND (sqlc.narg(thread)::text IS NULL OR t.thread_id = sqlc.narg(thread)::text)
  AND (sqlc.arg(status)::text = 'all' OR (sqlc.arg(status)::text = 'pending') = (t.status = 'pending'))
  AND (th.visibility IS DISTINCT FROM 'private'
       OR (sqlc.narg(viewer_user)::uuid IS NOT NULL AND th.created_by_user = sqlc.narg(viewer_user)::uuid)
       OR (sqlc.narg(viewer_service)::uuid IS NOT NULL AND th.created_by_service = sqlc.narg(viewer_service)::uuid))
ORDER BY t.requested_at DESC, t.id
LIMIT sqlc.arg(row_limit);

-- name: ListPendingToolApprovalsForUser :many
-- The inbox of a person: pending requests of agents of the teams they are
-- on, with their role there (the caller decides who may decide).
SELECT sqlc.embed(t), ag.name AS agent_name, ag.team_id, ag.service_managed, m.role AS member_role,
    th.visibility AS thread_visibility, th.created_by_user AS thread_user, th.created_by_service AS thread_service,
    th.title AS thread_title
FROM tool_approvals t
JOIN agents ag ON ag.id = t.agent_id
JOIN team_members m ON m.team_id = ag.team_id AND m.user_id = sqlc.arg(user_id)
LEFT JOIN agent_threads th ON th.agent_id = t.agent_id AND th.thread_id = t.thread_id
WHERE t.status = 'pending'
ORDER BY t.requested_at DESC, t.id
LIMIT 500;

-- name: ListPendingToolApprovalsForTeam :many
-- The inbox of a service account: pending requests of its team's agents.
SELECT sqlc.embed(t), ag.name AS agent_name, ag.team_id, ag.service_managed,
    th.visibility AS thread_visibility, th.created_by_user AS thread_user, th.created_by_service AS thread_service,
    th.title AS thread_title
FROM tool_approvals t
JOIN agents ag ON ag.id = t.agent_id
LEFT JOIN agent_threads th ON th.agent_id = t.agent_id AND th.thread_id = t.thread_id
WHERE t.status = 'pending' AND ag.team_id = sqlc.arg(team_id)
ORDER BY t.requested_at DESC, t.id
LIMIT 500;

-- name: DeleteThreadToolApprovals :exec
DELETE FROM tool_approvals WHERE agent_id = $1 AND thread_id = $2;
