-- Agents and the templates they are made from.
--
--   * agent_templates: admin-approved agent definitions (instructions and,
--     later, tools). Agents are only made from templates for now; teams
--     uploading their own agents comes later, behind sandboxed runtimes.
--   * agents: one durable Pi agent. It is either personal (one per person:
--     their assistant) or a team's. Its conversation state is not here: it
--     lives in its own schema in the agents database (AGENTS_DATABASE_URL),
--     reached with its own database role, whose password is stored sealed
--     with ENCRYPTION_KEY, as is the token the platform presents to its
--     runtime.

-- +goose Up
CREATE TABLE agent_templates (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    slug         text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9](?:[a-z0-9-]{0,62}[a-z0-9])?$'),
    name         text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    description  text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
    instructions text        NOT NULL CHECK (char_length(instructions) BETWEEN 1 AND 20000),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);

INSERT INTO agent_templates (slug, name, description, instructions) VALUES
    ('assistant', 'Assistant',
     'A general helper with a clock tool. Every person gets one as their personal assistant.',
     'You are a helpful, concise personal assistant. Use get_time for anything about the current date or time.');

CREATE TABLE agents (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    kind              text        NOT NULL CHECK (kind IN ('personal', 'team')),
    owner_user_id     uuid        REFERENCES users (id) ON DELETE CASCADE,
    team_id           uuid        REFERENCES teams (id) ON DELETE CASCADE,
    template_id       uuid        NOT NULL REFERENCES agent_templates (id),
    name              text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    description       text        NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
    -- Sealed with ENCRYPTION_KEY; associated data is the agent ID.
    db_password_enc   bytea       NOT NULL,
    runtime_token_enc bytea       NOT NULL,
    enabled           boolean     NOT NULL DEFAULT true,
    revision          bigint      NOT NULL DEFAULT 1,
    created_by        uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    -- Updated at most once a minute.
    last_used_at      timestamptz,
    CONSTRAINT agents_owner CHECK (
        (kind = 'personal' AND owner_user_id IS NOT NULL AND team_id IS NULL) OR
        (kind = 'team' AND team_id IS NOT NULL AND owner_user_id IS NULL))
);
-- One personal assistant per person.
CREATE UNIQUE INDEX agents_personal_key ON agents (owner_user_id) WHERE kind = 'personal';
-- Team agent names are unique in the team, ignoring case.
CREATE UNIQUE INDEX agents_team_name_key ON agents (team_id, lower(name)) WHERE kind = 'team';

-- +goose Down
DROP TABLE agents;
DROP TABLE agent_templates;
