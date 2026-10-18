-- Models agents can use, managed by platform admins.
--
--   * model_providers: where models are served. kind selects the API
--     (openai: OpenAI's Responses API; anthropic: Anthropic's Messages API;
--     openai-compatible: chat completions, as LiteLLM, vLLM or Ollama serve
--     it; openai-responses and anthropic-compatible: other servers of those
--     APIs). base_url follows the provider's SDK convention
--     (https://api.openai.com/v1, https://api.anthropic.com). The API key and
--     any extra headers are sealed with ENCRYPTION_KEY (associated data
--     "model_provider:<id>"). access_mode says how agents reach it: proxy
--     (through the platform, which adds the key and counts usage; agents never
--     see the key) or direct (the agent gets the URL and key).
--   * models: what a provider offers, published to every team or to chosen
--     teams (model_teams). Disabled models, or models of a disabled provider,
--     take no new use, and agents using them do not start.
--   * agent_template_versions.model_id: the model a version runs on (NULL:
--     the platform's default, AGENTS_MODEL_*). agents.model_id overrides it
--     for one agent.
--   * model_usage: tokens per call, recorded by the platform's model proxy
--     (source proxy) or reported by agents (source agent). Kept when the agent
--     or the model is deleted.

-- +goose Up
CREATE TABLE model_providers (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    kind        text        NOT NULL CHECK (kind IN ('openai', 'anthropic', 'openai-compatible', 'openai-responses', 'anthropic-compatible')),
    base_url    text        NOT NULL CHECK (char_length(base_url) BETWEEN 1 AND 2048),
    api_key_enc bytea       NOT NULL,
    headers_enc bytea,
    access_mode text        NOT NULL DEFAULT 'proxy' CHECK (access_mode IN ('proxy', 'direct')),
    enabled     boolean     NOT NULL DEFAULT true,
    revision    bigint      NOT NULL DEFAULT 1,
    created_by  uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX model_providers_name_key ON model_providers (lower(name));

CREATE TABLE models (
    id             uuid             PRIMARY KEY DEFAULT gen_random_uuid(),
    provider_id    uuid             NOT NULL REFERENCES model_providers (id) ON DELETE CASCADE,
    upstream_id    text             NOT NULL CHECK (char_length(upstream_id) BETWEEN 1 AND 200),
    name           text             NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    context_window integer          NOT NULL DEFAULT 128000 CHECK (context_window BETWEEN 1024 AND 100000000),
    max_tokens     integer          NOT NULL DEFAULT 16000 CHECK (max_tokens BETWEEN 1 AND 10000000),
    images         boolean          NOT NULL DEFAULT false,
    reasoning      boolean          NOT NULL DEFAULT false,
    -- USD per million tokens (optional, for usage costs).
    input_price    double precision CHECK (input_price >= 0),
    output_price   double precision CHECK (output_price >= 0),
    enabled        boolean          NOT NULL DEFAULT true,
    visibility     text             NOT NULL DEFAULT 'all' CHECK (visibility IN ('all', 'teams')),
    revision       bigint           NOT NULL DEFAULT 1,
    created_at     timestamptz      NOT NULL DEFAULT now(),
    updated_at     timestamptz      NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX models_provider_upstream_key ON models (provider_id, upstream_id);

CREATE TABLE model_teams (
    model_id uuid NOT NULL REFERENCES models (id) ON DELETE CASCADE,
    team_id  uuid NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    PRIMARY KEY (model_id, team_id)
);
CREATE INDEX model_teams_team ON model_teams (team_id);

-- A model in use by a template version or an agent cannot be deleted (disable it instead).
ALTER TABLE agent_template_versions ADD COLUMN model_id uuid REFERENCES models (id) ON DELETE RESTRICT;
ALTER TABLE agents ADD COLUMN model_id uuid REFERENCES models (id) ON DELETE RESTRICT;

CREATE TABLE model_usage (
    id                 bigint      GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    team_id            uuid        NOT NULL REFERENCES teams (id) ON DELETE CASCADE,
    agent_id           uuid        REFERENCES agents (id) ON DELETE SET NULL,
    model_id           uuid        REFERENCES models (id) ON DELETE SET NULL,
    source             text        NOT NULL CHECK (source IN ('proxy', 'agent')),
    input_tokens       bigint      NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
    output_tokens      bigint      NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
    cache_read_tokens  bigint      NOT NULL DEFAULT 0 CHECK (cache_read_tokens >= 0),
    cache_write_tokens bigint      NOT NULL DEFAULT 0 CHECK (cache_write_tokens >= 0),
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX model_usage_team_time ON model_usage (team_id, created_at);
CREATE INDEX model_usage_agent_time ON model_usage (agent_id, created_at) WHERE agent_id IS NOT NULL;

-- +goose Down
DROP TABLE model_usage;
ALTER TABLE agents DROP COLUMN model_id;
ALTER TABLE agent_template_versions DROP COLUMN model_id;
DROP TABLE model_teams;
DROP TABLE models;
DROP TABLE model_providers;
