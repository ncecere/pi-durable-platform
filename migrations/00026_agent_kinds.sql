-- Agent kinds (docs/first-party-agents.md): a template version says which of
-- the runtime's compositions its agents run, and that kind's settings.
--
--   * kind: assistant (the default, the platform's own agent), triage or
--     researcher. Part of the version, so upgrades and rollbacks change it like
--     the instructions; the runtime gets it at start (AGENT_KIND).
--   * kind_settings: the kind's settings as a JSON object, checked and stored
--     with its defaults by the platform (AGENT_KIND_SETTINGS): for triage the
--     MCP tools that list, read and write and whether writes ask first; for
--     the researcher background subagents and the plan's size.

-- +goose Up
ALTER TABLE agent_template_versions
    ADD COLUMN kind text NOT NULL DEFAULT 'assistant' CHECK (kind IN ('assistant', 'triage', 'researcher')),
    ADD COLUMN kind_settings jsonb NOT NULL DEFAULT '{}'
        CHECK (jsonb_typeof(kind_settings) = 'object' AND octet_length(kind_settings::text) <= 16384);

-- +goose Down
ALTER TABLE agent_template_versions
    DROP COLUMN kind_settings,
    DROP COLUMN kind;
