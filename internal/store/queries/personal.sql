-- name: GetPersonalSettings :one
SELECT * FROM personal_workspace_settings WHERE id;

-- name: UpdatePersonalSettings :one
UPDATE personal_workspace_settings SET enabled = $1, max_agents = $2, max_running = $3, monthly_budget_usd = $4,
       revision = revision + 1, updated_at = now()
WHERE id
RETURNING *;

-- name: GetPersonalTeam :one
SELECT * FROM teams WHERE personal_owner = $1;

-- name: InsertPersonalTeam :one
INSERT INTO teams (slug, name, kind, personal_owner, created_by)
VALUES ($1, $2, 'personal', $3, $3)
ON CONFLICT (personal_owner) DO NOTHING
RETURNING *;

-- name: SetPersonalLimits :one
UPDATE teams SET limit_max_agents = $2, limit_max_running = $3, limit_monthly_budget_usd = $4, revision = revision + 1, updated_at = now()
WHERE id = $1 AND kind = 'personal'
RETURNING *;

-- name: CountAgentsInTeam :one
SELECT count(*) FROM agents WHERE team_id = $1;

-- name: TeamAgentIDs :many
SELECT id FROM agents WHERE team_id = $1;
