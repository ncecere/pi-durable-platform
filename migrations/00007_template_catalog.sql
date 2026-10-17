-- Templates become the unit of governance.
--
--   * agent_templates: owned by the platform (team_id NULL: curated by
--     platform admins, always in the catalog) or by a team. A team template
--     is private to its team until a platform admin approves its team's
--     request to publish it (status private -> pending -> published; a
--     rejected request goes back to private with review_note). Archived
--     templates take no new deployments; existing agents keep working.
--   * agent_template_versions: immutable versions (instructions for now;
--     tools, models and connections later). A template's current_version is
--     what new agents get.
--   * agents.template_version: the version an agent runs, upgraded only on
--     purpose.

-- +goose Up
ALTER TABLE agent_templates ADD COLUMN team_id uuid REFERENCES teams (id) ON DELETE CASCADE;
ALTER TABLE agent_templates ADD COLUMN status text NOT NULL DEFAULT 'published'
    CHECK (status IN ('private', 'pending', 'published'));
ALTER TABLE agent_templates ADD COLUMN review_note text NOT NULL DEFAULT '' CHECK (char_length(review_note) <= 2000);
ALTER TABLE agent_templates ADD COLUMN archived boolean NOT NULL DEFAULT false;
ALTER TABLE agent_templates ADD COLUMN current_version integer NOT NULL DEFAULT 1 CHECK (current_version >= 1);
ALTER TABLE agent_templates ADD COLUMN revision bigint NOT NULL DEFAULT 1;
ALTER TABLE agent_templates ADD COLUMN created_by uuid REFERENCES users (id) ON DELETE SET NULL;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_platform_published CHECK (team_id IS NOT NULL OR status = 'published');
ALTER TABLE agent_templates DROP CONSTRAINT agent_templates_slug_key;
CREATE UNIQUE INDEX agent_templates_platform_slug ON agent_templates (slug) WHERE team_id IS NULL;
CREATE UNIQUE INDEX agent_templates_team_slug ON agent_templates (team_id, slug) WHERE team_id IS NOT NULL;
CREATE INDEX agent_templates_status ON agent_templates (status) WHERE status <> 'private';

CREATE TABLE agent_template_versions (
    template_id  uuid        NOT NULL REFERENCES agent_templates (id) ON DELETE CASCADE,
    version      integer     NOT NULL CHECK (version >= 1),
    instructions text        NOT NULL CHECK (char_length(instructions) BETWEEN 1 AND 20000),
    notes        text        NOT NULL DEFAULT '' CHECK (char_length(notes) <= 2000),
    created_by   uuid        REFERENCES users (id) ON DELETE SET NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (template_id, version)
);
INSERT INTO agent_template_versions (template_id, version, instructions, notes)
    SELECT id, 1, instructions, 'Initial version' FROM agent_templates;
ALTER TABLE agent_templates DROP COLUMN instructions;

ALTER TABLE agents ADD COLUMN template_version integer NOT NULL DEFAULT 1;
ALTER TABLE agents ADD CONSTRAINT agents_template_version_fkey
    FOREIGN KEY (template_id, template_version) REFERENCES agent_template_versions (template_id, version);
ALTER TABLE agents ALTER COLUMN template_version DROP DEFAULT;

-- +goose Down
ALTER TABLE agents DROP CONSTRAINT agents_template_version_fkey;
ALTER TABLE agents DROP COLUMN template_version;
ALTER TABLE agent_templates ADD COLUMN instructions text;
UPDATE agent_templates t SET instructions = v.instructions
    FROM agent_template_versions v WHERE v.template_id = t.id AND v.version = t.current_version;
ALTER TABLE agent_templates ALTER COLUMN instructions SET NOT NULL;
DROP TABLE agent_template_versions;
DROP INDEX agent_templates_status;
DROP INDEX agent_templates_team_slug;
DROP INDEX agent_templates_platform_slug;
DELETE FROM agent_templates WHERE team_id IS NOT NULL;
ALTER TABLE agent_templates ADD CONSTRAINT agent_templates_slug_key UNIQUE (slug);
ALTER TABLE agent_templates DROP CONSTRAINT agent_templates_platform_published;
ALTER TABLE agent_templates DROP COLUMN created_by;
ALTER TABLE agent_templates DROP COLUMN revision;
ALTER TABLE agent_templates DROP COLUMN current_version;
ALTER TABLE agent_templates DROP COLUMN archived;
ALTER TABLE agent_templates DROP COLUMN review_note;
ALTER TABLE agent_templates DROP COLUMN status;
ALTER TABLE agent_templates DROP COLUMN team_id;
