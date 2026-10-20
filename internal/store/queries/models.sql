-- name: ListModelProviders :many
-- Providers with their owning team (NULL columns: a platform provider):
-- every one, the platform's only (platform_only), or one team's (team_id).
SELECT p.*, (SELECT count(*) FROM models m WHERE m.provider_id = p.id) AS model_count,
       t.slug AS team_slug, t.name AS team_name, t.kind AS team_kind
FROM model_providers p LEFT JOIN teams t ON t.id = p.team_id
WHERE (NOT sqlc.arg(platform_only)::boolean OR p.team_id IS NULL)
  AND (sqlc.narg(team_id)::uuid IS NULL OR p.team_id = sqlc.narg(team_id)::uuid)
ORDER BY (p.team_id IS NOT NULL), lower(p.name), p.id;

-- name: GetModelProvider :one
SELECT * FROM model_providers WHERE id = $1;

-- name: LockModelProvider :one
SELECT * FROM model_providers WHERE id = $1 FOR UPDATE;

-- name: InsertModelProvider :one
INSERT INTO model_providers (id, name, kind, base_url, api_key_enc, headers_enc, access_mode, enabled, created_by, team_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
RETURNING *;

-- name: UpdateModelProvider :one
UPDATE model_providers SET name = $2, base_url = $3, api_key_enc = $4, headers_enc = $5, access_mode = $6, enabled = $7,
       revision = revision + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteModelProvider :exec
DELETE FROM model_providers WHERE id = $1;

-- name: ListModels :many
-- Every model, or one provider's, with its provider and the provider's
-- owning team (NULL columns: a platform provider); platform_only and
-- team_id filter by owner as ListModelProviders does.
SELECT sqlc.embed(m), p.name AS provider_name, p.kind AS provider_kind, p.enabled AS provider_enabled,
       p.access_mode AS provider_access_mode, p.team_id AS provider_team_id,
       t.slug AS team_slug, t.name AS team_name, t.kind AS team_kind
FROM models m JOIN model_providers p ON p.id = m.provider_id LEFT JOIN teams t ON t.id = p.team_id
WHERE (sqlc.narg(provider_id)::uuid IS NULL OR m.provider_id = sqlc.narg(provider_id)::uuid)
  AND (NOT sqlc.arg(platform_only)::boolean OR p.team_id IS NULL)
  AND (sqlc.narg(team_id)::uuid IS NULL OR p.team_id = sqlc.narg(team_id)::uuid)
ORDER BY (p.team_id IS NOT NULL), lower(p.name), lower(m.name), m.id;

-- name: GetModel :one
SELECT * FROM models WHERE id = $1;

-- name: LockModel :one
SELECT * FROM models WHERE id = $1 FOR UPDATE;

-- name: InsertModel :one
INSERT INTO models (provider_id, upstream_id, name, context_window, max_tokens, images, reasoning, input_price, output_price,
                    enabled, visibility, personal)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: UpdateModel :one
UPDATE models SET name = $2, context_window = $3, max_tokens = $4, images = $5, reasoning = $6, input_price = $7,
       output_price = $8, enabled = $9, visibility = $10, personal = $11, revision = revision + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteModel :exec
DELETE FROM models WHERE id = $1;

-- name: ListModelTeams :many
SELECT t.id, t.slug, t.name
FROM model_teams mt JOIN teams t ON t.id = mt.team_id
WHERE mt.model_id = $1
ORDER BY t.slug;

-- name: ClearModelTeams :exec
DELETE FROM model_teams WHERE model_id = $1;

-- name: AddModelTeam :exec
INSERT INTO model_teams (model_id, team_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: ListAvailableModels :many
-- The models a team may use now: enabled, of an enabled provider, and
-- either of the team's own providers, or of a platform provider and
-- published to every team or to this one (personal workspaces: published to
-- personal workspaces). A team provider's models are never available to
-- another team, whatever their visibility.
SELECT sqlc.embed(m), p.name AS provider_name, p.kind AS provider_kind, p.team_id AS provider_team_id
FROM models m JOIN model_providers p ON p.id = m.provider_id
WHERE m.enabled AND p.enabled
  AND (CASE WHEN p.team_id IS NOT NULL THEN p.team_id = sqlc.arg(team_id)::uuid
            WHEN (SELECT tk.kind FROM teams tk WHERE tk.id = sqlc.arg(team_id)) = 'personal' THEN m.personal
            ELSE m.visibility = 'all' OR EXISTS (SELECT 1 FROM model_teams mt WHERE mt.model_id = m.id AND mt.team_id = sqlc.arg(team_id)) END)
ORDER BY (p.team_id IS NULL), lower(p.name), lower(m.name), m.id;

-- name: GetAvailableModel :one
-- One model with its provider, if the team may use it now (the rule of
-- ListAvailableModels).
SELECT sqlc.embed(m), sqlc.embed(p)
FROM models m JOIN model_providers p ON p.id = m.provider_id
WHERE m.id = sqlc.arg(model_id) AND m.enabled AND p.enabled
  AND (CASE WHEN p.team_id IS NOT NULL THEN p.team_id = sqlc.arg(team_id)::uuid
            WHEN (SELECT tk.kind FROM teams tk WHERE tk.id = sqlc.arg(team_id)) = 'personal' THEN m.personal
            ELSE m.visibility = 'all' OR EXISTS (SELECT 1 FROM model_teams mt WHERE mt.model_id = m.id AND mt.team_id = sqlc.arg(team_id)) END);

-- name: GetModelSummary :one
SELECT m.id, m.name, m.enabled, p.name AS provider_name, p.enabled AS provider_enabled, p.team_id AS provider_team_id
FROM models m JOIN model_providers p ON p.id = m.provider_id
WHERE m.id = $1;

-- name: GetModelSettings :one
SELECT * FROM model_settings WHERE id = 1;

-- name: LockModelSettings :one
SELECT * FROM model_settings WHERE id = 1 FOR UPDATE;

-- name: UpdateModelSettings :one
UPDATE model_settings SET teams_may_add_providers = $1, private_networks = $2, updated_by = $3, teams_may_add_mcp_servers = $4,
       revision = revision + 1, updated_at = now()
WHERE id = 1
RETURNING *;

-- name: InsertModelUsage :exec
INSERT INTO model_usage (team_id, agent_id, model_id, source, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, oauth_grant_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: InsertModelUsageBatch :exec
INSERT INTO model_usage (team_id, agent_id, model_id, source, requests, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens, oauth_grant_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: UsageFacts :many
-- Usage summed per UTC day, team, agent and model in [since, until), for one
-- team (or every team), optionally some agents only (agent_ids; with
-- with_managed false, service-managed agents among them match nothing), or
-- one connected app's (the rows attributed to grants of a client). Prices
-- are the models' current ones (USD per million tokens).
SELECT (u.created_at AT TIME ZONE 'UTC')::date AS day,
       u.team_id, t.slug AS team_slug, t.name AS team_name,
       u.agent_id, a.name AS agent_name, COALESCE(a.service_managed, false)::boolean AS agent_managed,
       u.model_id, m.name AS model_name, p.name AS provider_name, m.input_price, m.output_price,
       sum(u.requests)::bigint AS requests, sum(u.input_tokens)::bigint AS input_tokens,
       sum(u.output_tokens)::bigint AS output_tokens, sum(u.cache_read_tokens)::bigint AS cache_read_tokens,
       sum(u.cache_write_tokens)::bigint AS cache_write_tokens
FROM model_usage u
JOIN teams t ON t.id = u.team_id
LEFT JOIN agents a ON a.id = u.agent_id
LEFT JOIN models m ON m.id = u.model_id
LEFT JOIN model_providers p ON p.id = m.provider_id
WHERE u.created_at >= sqlc.arg(since) AND u.created_at < sqlc.arg(until)
  AND (sqlc.narg(team_id)::uuid IS NULL OR u.team_id = sqlc.narg(team_id)::uuid)
  AND (sqlc.narg(agent_ids)::uuid[] IS NULL
       OR (u.agent_id = ANY(sqlc.narg(agent_ids)::uuid[]) AND (sqlc.arg(with_managed)::boolean OR NOT a.service_managed)))
  AND (sqlc.narg(oauth_client_id)::text IS NULL
       OR u.oauth_grant_id IN (SELECT g.id FROM oauth_grants g WHERE g.client_id = sqlc.narg(oauth_client_id)::text))
GROUP BY 1, u.team_id, t.slug, t.name, u.agent_id, a.name, a.service_managed, u.model_id, m.name, p.name, m.input_price, m.output_price
ORDER BY 1;
