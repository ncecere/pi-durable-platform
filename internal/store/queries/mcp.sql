-- name: ListMCPServers :many
-- Servers with their owning team (NULL columns: a platform server): every
-- one, the platform's only (platform_only), or one team's (team_id).
SELECT sqlc.embed(s), t.slug AS team_slug, t.name AS team_name, t.kind AS team_kind
FROM mcp_servers s LEFT JOIN teams t ON t.id = s.team_id
WHERE (NOT sqlc.arg(platform_only)::boolean OR s.team_id IS NULL)
  AND (sqlc.narg(team_id)::uuid IS NULL OR s.team_id = sqlc.narg(team_id)::uuid)
ORDER BY (s.team_id IS NOT NULL), lower(s.name), s.id;

-- name: GetMCPServer :one
SELECT * FROM mcp_servers WHERE id = $1;

-- name: LockMCPServer :one
SELECT * FROM mcp_servers WHERE id = $1 FOR UPDATE;

-- name: InsertMCPServer :one
INSERT INTO mcp_servers (id, name, slug, description, url, headers_enc, access_mode, enabled, visibility, created_by, personal, team_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING *;

-- name: UpdateMCPServer :one
UPDATE mcp_servers SET name = $2, description = $3, url = $4, headers_enc = $5, access_mode = $6, enabled = $7, visibility = $8,
       personal = $9, revision = revision + 1, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: SetMCPServerTools :one
UPDATE mcp_servers SET tools = $2, tools_refreshed_at = now(), tools_error = $3, updated_at = now()
WHERE id = $1
RETURNING *;

-- name: DeleteMCPServer :exec
DELETE FROM mcp_servers WHERE id = $1;

-- name: ListMCPServerTeams :many
SELECT t.id, t.slug, t.name
FROM mcp_server_teams st JOIN teams t ON t.id = st.team_id
WHERE st.server_id = $1
ORDER BY t.slug;

-- name: ClearMCPServerTeams :exec
DELETE FROM mcp_server_teams WHERE server_id = $1;

-- name: AddMCPServerTeam :exec
INSERT INTO mcp_server_teams (server_id, team_id) VALUES ($1, $2) ON CONFLICT DO NOTHING;

-- name: ListAvailableMCPServers :many
-- The MCP servers a team may use now: enabled, and either the team's own, or
-- a platform server published to every team or to it (personal workspaces:
-- published to personal workspaces). A team's server is never available to
-- another team, whatever its visibility.
SELECT * FROM mcp_servers s
WHERE s.enabled
  AND (CASE WHEN s.team_id IS NOT NULL THEN s.team_id = sqlc.arg(team_id)::uuid
            WHEN (SELECT tk.kind FROM teams tk WHERE tk.id = sqlc.arg(team_id)) = 'personal' THEN s.personal
            ELSE s.visibility = 'all' OR EXISTS (SELECT 1 FROM mcp_server_teams st WHERE st.server_id = s.id AND st.team_id = sqlc.arg(team_id)) END)
ORDER BY (s.team_id IS NULL), lower(s.name), s.id;

-- name: MCPServerAvailable :one
-- Whether the team may use the server now (the rule of ListAvailableMCPServers).
SELECT EXISTS (
    SELECT 1 FROM mcp_servers s
    WHERE s.id = sqlc.arg(server_id) AND s.enabled
      AND (CASE WHEN s.team_id IS NOT NULL THEN s.team_id = sqlc.arg(team_id)::uuid
                WHEN (SELECT tk.kind FROM teams tk WHERE tk.id = sqlc.arg(team_id)) = 'personal' THEN s.personal
                ELSE s.visibility = 'all' OR EXISTS (SELECT 1 FROM mcp_server_teams st WHERE st.server_id = s.id AND st.team_id = sqlc.arg(team_id)) END)
)::boolean;

-- name: TemplateVersionUsesTeamMCP :one
-- Whether a template version gives its agents a team's own MCP server.
SELECT EXISTS (
    SELECT 1 FROM template_version_mcp vm JOIN mcp_servers s ON s.id = vm.server_id
    WHERE vm.template_id = $1 AND vm.version = $2 AND s.team_id IS NOT NULL
)::boolean;

-- name: InsertTemplateVersionMCP :exec
INSERT INTO template_version_mcp (template_id, version, server_id, tools) VALUES ($1, $2, $3, $4);

-- name: ListTemplateVersionMCP :many
SELECT vm.server_id, vm.tools, s.name, s.slug
FROM template_version_mcp vm JOIN mcp_servers s ON s.id = vm.server_id
WHERE vm.template_id = $1 AND vm.version = $2
ORDER BY lower(s.name);

-- name: GetTemplateVersionMCP :one
SELECT * FROM template_version_mcp WHERE template_id = $1 AND version = $2 AND server_id = $3;

-- name: UpsertThreadMCPServer :one
INSERT INTO thread_mcp_servers (agent_id, thread_id, name, slug, url, headers_enc, expires_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (agent_id, thread_id, slug) DO UPDATE SET name = EXCLUDED.name, url = EXCLUDED.url, headers_enc = EXCLUDED.headers_enc,
    expires_at = EXCLUDED.expires_at
RETURNING *;

-- name: DeleteThreadMCPServersExcept :exec
-- Removes a thread's servers other than those named (all, with an empty list).
DELETE FROM thread_mcp_servers WHERE agent_id = $1 AND thread_id = $2 AND NOT (slug = ANY(sqlc.arg(keep)::text[]));

-- name: ListThreadMCPServers :many
-- A thread's servers that have not expired (every thread's, with thread_id NULL).
SELECT * FROM thread_mcp_servers
WHERE agent_id = sqlc.arg(agent_id) AND (sqlc.narg(thread_id)::text IS NULL OR thread_id = sqlc.narg(thread_id)::text) AND expires_at > now()
ORDER BY thread_id, slug;

-- name: GetThreadMCPServer :one
SELECT * FROM thread_mcp_servers WHERE id = $1;

-- name: DeleteExpiredThreadMCPServers :execrows
DELETE FROM thread_mcp_servers WHERE expires_at <= now() - interval '1 day';

-- name: InsertMCPCall :exec
INSERT INTO mcp_calls (team_id, agent_id, server_id, server_name, client, tool, ok, duration_ms, oauth_grant_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);
