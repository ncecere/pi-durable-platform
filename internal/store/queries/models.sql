-- name: ListModelProviders :many
SELECT p.*, (SELECT count(*) FROM models m WHERE m.provider_id = p.id) AS model_count
FROM model_providers p
ORDER BY lower(p.name), p.id;

-- name: GetModelProvider :one
SELECT * FROM model_providers WHERE id = $1;

-- name: LockModelProvider :one
SELECT * FROM model_providers WHERE id = $1 FOR UPDATE;

-- name: InsertModelProvider :one
INSERT INTO model_providers (id, name, kind, base_url, api_key_enc, headers_enc, access_mode, enabled, created_by)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: UpdateModelProvider :one
UPDATE model_providers SET name = $2, base_url = $3, api_key_enc = $4, headers_enc = $5, access_mode = $6, enabled = $7,
       revision = revision + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteModelProvider :exec
DELETE FROM model_providers WHERE id = $1;

-- name: ListModels :many
-- Every model, or one provider's, with its provider.
SELECT sqlc.embed(m), p.name AS provider_name, p.kind AS provider_kind, p.enabled AS provider_enabled,
       p.access_mode AS provider_access_mode
FROM models m JOIN model_providers p ON p.id = m.provider_id
WHERE sqlc.narg(provider_id)::uuid IS NULL OR m.provider_id = sqlc.narg(provider_id)::uuid
ORDER BY lower(p.name), lower(m.name), m.id;

-- name: GetModel :one
SELECT * FROM models WHERE id = $1;

-- name: LockModel :one
SELECT * FROM models WHERE id = $1 FOR UPDATE;

-- name: InsertModel :one
INSERT INTO models (provider_id, upstream_id, name, context_window, max_tokens, images, reasoning, input_price, output_price,
                    enabled, visibility)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
RETURNING *;

-- name: UpdateModel :one
UPDATE models SET name = $2, context_window = $3, max_tokens = $4, images = $5, reasoning = $6, input_price = $7,
       output_price = $8, enabled = $9, visibility = $10, revision = revision + 1, updated_at = now()
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
-- published to every team or to this one.
SELECT sqlc.embed(m), p.name AS provider_name, p.kind AS provider_kind
FROM models m JOIN model_providers p ON p.id = m.provider_id
WHERE m.enabled AND p.enabled
  AND (m.visibility = 'all' OR EXISTS (SELECT 1 FROM model_teams mt WHERE mt.model_id = m.id AND mt.team_id = sqlc.arg(team_id)))
ORDER BY lower(p.name), lower(m.name), m.id;

-- name: GetAvailableModel :one
-- One model with its provider, if the team may use it now.
SELECT sqlc.embed(m), sqlc.embed(p)
FROM models m JOIN model_providers p ON p.id = m.provider_id
WHERE m.id = sqlc.arg(model_id) AND m.enabled AND p.enabled
  AND (m.visibility = 'all' OR EXISTS (SELECT 1 FROM model_teams mt WHERE mt.model_id = m.id AND mt.team_id = sqlc.arg(team_id)));

-- name: GetModelSummary :one
SELECT m.id, m.name, m.enabled, p.name AS provider_name, p.enabled AS provider_enabled
FROM models m JOIN model_providers p ON p.id = m.provider_id
WHERE m.id = $1;

-- name: InsertModelUsage :exec
INSERT INTO model_usage (team_id, agent_id, model_id, source, input_tokens, output_tokens, cache_read_tokens, cache_write_tokens)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8);
