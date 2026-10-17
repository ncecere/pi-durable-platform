-- name: InsertAgent :one
INSERT INTO agents (id, team_id, template_id, template_version, name, description, db_password_enc, runtime_token_enc, created_by, external_key)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetAgentByExternalKey :one
SELECT * FROM agents WHERE team_id = $1 AND external_key = $2;

-- name: GetAgent :one
SELECT * FROM agents WHERE id = $1;

-- name: LockAgent :one
SELECT * FROM agents WHERE id = $1 FOR UPDATE;

-- name: ListAgentsForUser :many
-- The agents of the teams the person belongs to (service-managed ones only
-- for teams where they are an owner or admin, and only when asked).
SELECT a.*, t.slug AS team_slug, t.name AS team_name, tpl.slug AS template_slug, tpl.name AS template_name,
       tpl.current_version AS template_current_version
FROM agents a
JOIN agent_templates tpl ON tpl.id = a.template_id
JOIN teams t ON t.id = a.team_id
JOIN team_members m ON m.team_id = a.team_id AND m.user_id = sqlc.arg(user_id)
WHERE t.status = 'active'
  AND (a.external_key IS NULL OR (sqlc.arg(include_managed)::boolean AND m.role IN ('owner', 'admin')))
ORDER BY t.slug, lower(a.name), a.id;

-- name: AgentIDs :many
SELECT id FROM agents;

-- name: ListTeamAgents :many
-- A team's agents; service-managed ones (with an external key) only when asked.
SELECT * FROM agents
WHERE team_id = $1 AND (sqlc.arg(include_managed)::boolean OR external_key IS NULL)
ORDER BY lower(name), id;

-- name: CountTeamAgents :one
SELECT count(*) FROM agents WHERE team_id = $1 AND external_key IS NULL;

-- name: UpdateAgent :one
UPDATE agents SET name = $2, description = $3, enabled = $4, template_version = $5, revision = revision + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateAgentSecrets :exec
UPDATE agents SET db_password_enc = $2, runtime_token_enc = $3, updated_at = now() WHERE id = $1;

-- name: TouchAgent :exec
UPDATE agents SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: DeleteAgent :exec
DELETE FROM agents WHERE id = $1;
