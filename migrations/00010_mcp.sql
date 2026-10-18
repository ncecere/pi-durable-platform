-- MCP servers agents can use.
--
--   * mcp_servers: remote MCP servers (Streamable HTTP) that platform admins
--     add and publish to every team or chosen teams (mcp_server_teams). Extra
--     headers (for example Authorization) are sealed with ENCRYPTION_KEY
--     (associated data "mcp_server:<id>"). tools is the server's tool list as
--     last read by the platform. access_mode: proxy (agents call the
--     platform's gateway, which adds the headers, checks the tool and logs
--     the call) or direct (agents get the URL and headers). slug names its
--     tools for the model (mcp__<slug>__<tool>).
--   * template_version_mcp: the servers a template version's agents get,
--     optionally only some of their tools.
--   * agent_template_versions.client_mcp: whether apps may give its agents
--     their own MCP servers for a thread (thread_mcp_servers: sealed headers,
--     an expiry; reached through the gateway or directly, as the platform is
--     configured, and only on allowed hosts).
--   * mcp_calls: tool calls made through the gateway.

-- +goose Up
CREATE TABLE mcp_servers (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name               text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    slug               text        NOT NULL CHECK (slug ~ '^[a-z0-9](?:[a-z0-9_]{0,30}[a-z0-9])?$'),
    description        text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
    url                text        NOT NULL CHECK (char_length(url) BETWEEN 1 AND 2048),
    headers_enc        bytea,
    access_mode        text        NOT NULL DEFAULT 'proxy' CHECK (access_mode IN ('proxy', 'direct')),
    enabled            boolean     NOT NULL DEFAULT true,
    visibility         text        NOT NULL DEFAULT 'all' CHECK (visibility IN ('all', 'teams')),
    tools              jsonb       NOT NULL DEFAULT '[]',
    tools_refreshed_at timestamptz,
    tools_error        text,
    revision           bigint      NOT NULL DEFAULT 1,
    created_by         uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX mcp_servers_name_key ON mcp_servers (lower(name));
CREATE UNIQUE INDEX mcp_servers_slug_key ON mcp_servers (slug);

CREATE TABLE mcp_server_teams (
    server_id uuid NOT NULL REFERENCES mcp_servers (id) ON DELETE CASCADE,
    team_id   uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    PRIMARY KEY (server_id, team_id)
);
CREATE INDEX mcp_server_teams_team ON mcp_server_teams (team_id);

CREATE TABLE template_version_mcp (
    template_id uuid    NOT NULL,
    version     integer NOT NULL,
    server_id   uuid    NOT NULL REFERENCES mcp_servers (id) ON DELETE RESTRICT,
    -- NULL: every tool of the server.
    tools       text[],
    PRIMARY KEY (template_id, version, server_id),
    FOREIGN KEY (template_id, version) REFERENCES agent_template_versions (template_id, version) ON DELETE CASCADE
);
CREATE INDEX template_version_mcp_server ON template_version_mcp (server_id);

ALTER TABLE agent_template_versions ADD COLUMN client_mcp boolean NOT NULL DEFAULT false;

CREATE TABLE thread_mcp_servers (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    agent_id    uuid        NOT NULL REFERENCES agents (id) ON DELETE CASCADE,
    thread_id   text        NOT NULL CHECK (thread_id ~ '^[A-Za-z0-9_-]{1,64}$'),
    name        text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    slug        text        NOT NULL CHECK (slug ~ '^[a-z0-9](?:[a-z0-9_]{0,30}[a-z0-9])?$'),
    url         text        NOT NULL CHECK (char_length(url) BETWEEN 1 AND 2048),
    headers_enc bytea,
    expires_at  timestamptz NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (agent_id, thread_id, slug)
);
CREATE INDEX thread_mcp_servers_expiry ON thread_mcp_servers (expires_at);

CREATE TABLE mcp_calls (
    id          bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id     uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    agent_id    uuid        REFERENCES agents (id) ON DELETE SET NULL,
    server_id   uuid        REFERENCES mcp_servers (id) ON DELETE SET NULL,
    server_name text        NOT NULL,
    client      boolean     NOT NULL DEFAULT false,
    tool        text        NOT NULL,
    ok          boolean     NOT NULL,
    duration_ms integer     NOT NULL DEFAULT 0,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mcp_calls_team_time ON mcp_calls (team_id, created_at);

-- +goose Down
DROP TABLE mcp_calls;
DROP TABLE thread_mcp_servers;
ALTER TABLE agent_template_versions DROP COLUMN client_mcp;
DROP TABLE template_version_mcp;
DROP TABLE mcp_server_teams;
DROP TABLE mcp_servers;
