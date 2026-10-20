-- name: ListDeployableTemplates :many
-- What a team can deploy: the catalog (published, not archived) and the
-- team's own templates that aren't archived.
SELECT sqlc.embed(tpl), t.slug AS team_slug, t.name AS team_name
FROM agent_templates tpl
LEFT JOIN teams t ON t.id = tpl.team_id
WHERE NOT tpl.archived AND (tpl.status = 'published' OR tpl.team_id = sqlc.narg(team_id)::uuid)
ORDER BY (tpl.team_id IS NOT NULL), lower(tpl.name), tpl.id;

-- name: ListTeamTemplates :many
SELECT sqlc.embed(tpl), t.slug AS team_slug, t.name AS team_name
FROM agent_templates tpl JOIN teams t ON t.id = tpl.team_id
WHERE tpl.team_id = $1
ORDER BY lower(tpl.name), tpl.id;

-- name: ListTemplatesAdmin :many
-- Every template; with status, only those (for example the review queue).
SELECT sqlc.embed(tpl), t.slug AS team_slug, t.name AS team_name
FROM agent_templates tpl LEFT JOIN teams t ON t.id = tpl.team_id
WHERE sqlc.narg(status)::text IS NULL OR tpl.status = sqlc.narg(status)::text
ORDER BY tpl.updated_at DESC, tpl.id;

-- name: GetTemplateView :one
SELECT tpl.*, t.slug AS team_slug, t.name AS team_name,
       (SELECT count(*) FROM agents a WHERE a.template_id = tpl.id) AS agent_count
FROM agent_templates tpl LEFT JOIN teams t ON t.id = tpl.team_id
WHERE tpl.id = $1;

-- name: GetAgentTemplate :one
SELECT * FROM agent_templates WHERE id = $1;

-- name: GetPlatformTemplateBySlug :one
SELECT * FROM agent_templates WHERE team_id IS NULL AND slug = $1;

-- name: LockTemplate :one
SELECT * FROM agent_templates WHERE id = $1 FOR UPDATE;

-- name: InsertTemplate :one
INSERT INTO agent_templates (team_id, slug, name, description, status, created_by)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: UpdateTemplate :one
UPDATE agent_templates SET name = $2, description = $3, archived = $4, revision = revision + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetTemplateStatus :one
UPDATE agent_templates SET status = $2, review_note = $3, revision = revision + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: BumpTemplateVersion :one
UPDATE agent_templates SET current_version = current_version + 1, revision = revision + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: InsertTemplateVersion :one
INSERT INTO agent_template_versions (template_id, version, instructions, notes, created_by, model_id, client_mcp,
    approval_tools, approval_role, approval_timeout_seconds, agent_schedules, kind, kind_settings)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
RETURNING *;

-- name: ListTemplateVersions :many
SELECT v.*, u.display_name AS created_by_name, m.name AS model_name, p.name AS model_provider_name
FROM agent_template_versions v
LEFT JOIN users u ON u.id = v.created_by
LEFT JOIN models m ON m.id = v.model_id
LEFT JOIN model_providers p ON p.id = m.provider_id
WHERE v.template_id = $1
ORDER BY v.version DESC;

-- name: GetTemplateVersion :one
SELECT * FROM agent_template_versions WHERE template_id = $1 AND version = $2;
