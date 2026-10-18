-- Team MCP servers: a team's owners and admins add MCP servers for their own
-- team, as platform admins do for the platform (team model providers'
-- design, migration 00014).
--
--   * mcp_servers.team_id: the team that owns the server (NULL: a platform
--     server). It is for that team only: availability ignores its
--     visibility, mcp_server_teams and personal flag. Names are unique per
--     owner. Slugs stay unique across every owner: a slug names the server's
--     tools in an agent's tool namespace (mcp__<slug>__<tool>), where a
--     team's agents meet the platform's servers and the team's own, so a team
--     server may not take a slug the platform (or another team) uses. Team
--     servers are proxy-only (direct mode would hand agents the URL and
--     headers, and need egress rules a team can't grant), and every request
--     to one goes through the address guard (internal/teamnet).
--   * model_settings.teams_may_add_mcp_servers: whether teams may add MCP
--     servers. model_settings.private_networks is the one list of private
--     networks for every kind of team service: model providers and MCP
--     servers.
--   * personal_workspace_settings.own_mcp_servers: personal workspaces may
--     add their own MCP servers too.
--
-- Deleting a team deletes its servers. Its templates (whose versions use
-- them, ON DELETE RESTRICT) are deleted by the same cascade first: their
-- foreign key to teams is older, and PostgreSQL runs a statement's cascades
-- in that order, a level at a time. A version's servers
-- (template_version_mcp) are two levels below the team (template, version)
-- while the servers' RESTRICT check is one level below it, so
-- template_version_mcp also cascades from its template directly (the
-- version's key already implies it), and goes before that check.

-- +goose Up
ALTER TABLE mcp_servers ADD COLUMN team_id uuid REFERENCES teams (id) ON DELETE CASCADE;
ALTER TABLE mcp_servers ADD CONSTRAINT mcp_servers_team_proxy CHECK (team_id IS NULL OR access_mode = 'proxy');
DROP INDEX mcp_servers_name_key;
CREATE UNIQUE INDEX mcp_servers_name_key
    ON mcp_servers (COALESCE(team_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name));
CREATE INDEX mcp_servers_team ON mcp_servers (team_id) WHERE team_id IS NOT NULL;

ALTER TABLE model_settings ADD COLUMN teams_may_add_mcp_servers boolean NOT NULL DEFAULT true;

ALTER TABLE personal_workspace_settings ADD COLUMN own_mcp_servers boolean NOT NULL DEFAULT false;

ALTER TABLE template_version_mcp ADD CONSTRAINT template_version_mcp_template_fkey
    FOREIGN KEY (template_id) REFERENCES agent_templates (id) ON DELETE CASCADE;

-- +goose Down
ALTER TABLE template_version_mcp DROP CONSTRAINT template_version_mcp_template_fkey;
ALTER TABLE personal_workspace_settings DROP COLUMN own_mcp_servers;
ALTER TABLE model_settings DROP COLUMN teams_may_add_mcp_servers;
-- Team servers go, and the template versions' use of them.
DELETE FROM template_version_mcp WHERE server_id IN (SELECT id FROM mcp_servers WHERE team_id IS NOT NULL);
DELETE FROM mcp_servers WHERE team_id IS NOT NULL;
DROP INDEX mcp_servers_team;
DROP INDEX mcp_servers_name_key;
CREATE UNIQUE INDEX mcp_servers_name_key ON mcp_servers (lower(name));
ALTER TABLE mcp_servers DROP CONSTRAINT mcp_servers_team_proxy;
ALTER TABLE mcp_servers DROP COLUMN team_id;
