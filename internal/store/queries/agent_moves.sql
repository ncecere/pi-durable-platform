-- name: AgentMoveThreads :one
-- What moving an agent to the target team does to its threads: shared ones
-- (with a message) become visible to the target team; private ones stay
-- their creator's, and are out of reach while the creator can't use the agent
-- there (a service account of another team, a person who isn't a member).
SELECT count(*) FILTER (WHERE visibility = 'shared' AND NOT reserved)::bigint AS shared,
       count(*) FILTER (WHERE visibility = 'private')::bigint AS private,
       count(*) FILTER (WHERE visibility = 'private' AND NOT (created_by_user IS NOT NULL AND EXISTS (
           SELECT 1 FROM team_members m WHERE m.team_id = sqlc.arg(target_team) AND m.user_id = agent_threads.created_by_user)))::bigint
           AS private_unreachable
FROM agent_threads
WHERE agent_id = sqlc.arg(agent_id);

-- name: AgentMoveAccess :one
-- Who may use a (shared, not service-managed) agent before and after a move.
SELECT (SELECT count(*) FROM team_members m WHERE m.team_id = sqlc.arg(target_team))::bigint AS target_members,
       (SELECT count(*) FROM service_accounts s WHERE s.team_id = sqlc.arg(target_team))::bigint AS target_service_accounts,
       (SELECT count(*) FROM team_members m WHERE m.team_id = sqlc.arg(source_team) AND NOT EXISTS (
           SELECT 1 FROM team_members t WHERE t.team_id = sqlc.arg(target_team) AND t.user_id = m.user_id))::bigint AS people_losing,
       (SELECT count(*) FROM service_accounts s WHERE s.team_id = sqlc.arg(source_team))::bigint AS service_accounts_losing;

-- name: CountAgentThreadMCPServers :one
SELECT count(*) FROM thread_mcp_servers WHERE agent_id = $1;

-- name: DeleteAgentThreadMCPServers :execrows
DELETE FROM thread_mcp_servers WHERE agent_id = $1;

-- name: AgentNameTaken :one
-- Whether another agent of the team (made by hand) has the name.
SELECT EXISTS (
    SELECT 1 FROM agents
    WHERE team_id = sqlc.arg(team_id) AND external_key IS NULL AND lower(name) = lower(sqlc.arg(name)) AND id <> sqlc.arg(id)
);

-- name: MoveAgent :one
-- The agent belongs to another team from now on. Its external key is
-- dropped (the binding was the old team's), and it is no longer
-- service-managed.
UPDATE agents
SET team_id = sqlc.arg(team_id), model_id = sqlc.arg(model_id), external_key = NULL, service_managed = false,
    revision = revision + 1, updated_at = now()
WHERE id = sqlc.arg(id)
RETURNING *;
