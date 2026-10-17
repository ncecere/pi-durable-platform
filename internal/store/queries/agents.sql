-- name: ListAgentTemplates :many
SELECT * FROM agent_templates ORDER BY name, id;

-- name: GetAgentTemplateBySlug :one
SELECT * FROM agent_templates WHERE slug = $1;

-- name: GetAgentTemplate :one
SELECT * FROM agent_templates WHERE id = $1;

-- name: InsertAgent :one
INSERT INTO agents (id, kind, owner_user_id, team_id, template_id, name, description, db_password_enc, runtime_token_enc, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: GetAgent :one
SELECT * FROM agents WHERE id = $1;

-- name: LockAgent :one
SELECT * FROM agents WHERE id = $1 FOR UPDATE;

-- name: GetPersonalAgent :one
SELECT * FROM agents WHERE kind = 'personal' AND owner_user_id = $1;

-- name: ListAgentsForUser :many
-- The person's own assistant and the agents of teams they belong to.
SELECT a.*, t.slug AS team_slug, t.name AS team_name, tpl.slug AS template_slug, tpl.name AS template_name
FROM agents a
JOIN agent_templates tpl ON tpl.id = a.template_id
LEFT JOIN teams t ON t.id = a.team_id
WHERE (a.kind = 'personal' AND a.owner_user_id = sqlc.arg(user_id))
   OR (a.kind = 'team' AND t.status = 'active' AND EXISTS (
        SELECT 1 FROM team_members m WHERE m.team_id = a.team_id AND m.user_id = sqlc.arg(user_id)))
ORDER BY a.kind DESC, t.slug NULLS FIRST, lower(a.name), a.id;

-- name: ListTeamAgents :many
SELECT * FROM agents WHERE team_id = $1 ORDER BY lower(name), id;

-- name: CountTeamAgents :one
SELECT count(*) FROM agents WHERE team_id = $1;

-- name: UpdateAgent :one
UPDATE agents SET name = $2, description = $3, enabled = $4, revision = revision + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: UpdateAgentSecrets :exec
UPDATE agents SET db_password_enc = $2, runtime_token_enc = $3, updated_at = now() WHERE id = $1;

-- name: TouchAgent :exec
UPDATE agents SET last_used_at = now()
WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute');

-- name: DeleteAgent :exec
DELETE FROM agents WHERE id = $1;
