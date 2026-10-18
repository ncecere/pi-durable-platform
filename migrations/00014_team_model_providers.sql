-- Team model providers: a team's owners and admins add providers and models
-- for their own team, as platform admins do for the platform.
--
--   * model_providers.team_id: the team that owns the provider (NULL: a
--     platform provider). Its models are for that team only: availability
--     ignores their visibility, model_teams and personal flag. Names are
--     unique per owner. Team providers are proxy-only (direct mode would need
--     per-agent egress rules a team can't grant), and every request to one
--     goes through the address guard (internal/netguard).
--   * model_settings: one row of platform-wide settings, set by platform
--     admins: whether teams may add providers, and the private networks
--     (CIDRs) team providers may live on (public addresses are always
--     allowed; loopback, link-local and metadata addresses never).
--   * personal_workspace_settings.own_providers: personal workspaces may add
--     their own providers too.
--
-- Deleting a team deletes its providers and their models. Its agents and
-- templates (which reference those models, ON DELETE RESTRICT) are deleted
-- by the same cascade first: their foreign keys to teams are older, and
-- PostgreSQL runs a statement's cascades in that order.

-- +goose Up
ALTER TABLE model_providers ADD COLUMN team_id uuid REFERENCES teams (id) ON DELETE CASCADE;
ALTER TABLE model_providers ADD CONSTRAINT model_providers_team_proxy CHECK (team_id IS NULL OR access_mode = 'proxy');
DROP INDEX model_providers_name_key;
CREATE UNIQUE INDEX model_providers_name_key
    ON model_providers (COALESCE(team_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name));
CREATE INDEX model_providers_team ON model_providers (team_id) WHERE team_id IS NOT NULL;

CREATE TABLE model_settings (
    id                      smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    teams_may_add_providers boolean     NOT NULL DEFAULT true,
    private_networks        text[]      NOT NULL DEFAULT '{}' CHECK (cardinality(private_networks) <= 50),
    revision                bigint      NOT NULL DEFAULT 1,
    updated_by              uuid        REFERENCES users (id) ON DELETE SET NULL,
    updated_at              timestamptz NOT NULL DEFAULT now()
);
INSERT INTO model_settings (id) VALUES (1);

ALTER TABLE personal_workspace_settings ADD COLUMN own_providers boolean NOT NULL DEFAULT false;

-- +goose Down
ALTER TABLE personal_workspace_settings DROP COLUMN own_providers;
DROP TABLE model_settings;
-- Team providers go: what used their models falls back to the default model.
UPDATE agents SET model_id = NULL
WHERE model_id IN (SELECT m.id FROM models m JOIN model_providers p ON p.id = m.provider_id WHERE p.team_id IS NOT NULL);
UPDATE agent_template_versions SET model_id = NULL
WHERE model_id IN (SELECT m.id FROM models m JOIN model_providers p ON p.id = m.provider_id WHERE p.team_id IS NOT NULL);
DELETE FROM model_providers WHERE team_id IS NOT NULL;
DROP INDEX model_providers_team;
DROP INDEX model_providers_name_key;
CREATE UNIQUE INDEX model_providers_name_key ON model_providers (lower(name));
ALTER TABLE model_providers DROP CONSTRAINT model_providers_team_proxy;
ALTER TABLE model_providers DROP COLUMN team_id;
