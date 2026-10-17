-- Agents belong to teams. The platform is where teams deploy, test and manage
-- agents; people reach agents through the team's own apps (with service
-- account keys), not through personal assistants here.
--
-- Personal agents (development builds only) are removed; their storage in the
-- agents database is removed by `pi-durable-platform prune-agent-storage`.

-- +goose Up
DELETE FROM agents WHERE kind = 'personal';
DROP INDEX agents_personal_key;
ALTER TABLE agents DROP CONSTRAINT agents_owner;
ALTER TABLE agents DROP CONSTRAINT agents_external_key_team;
ALTER TABLE agents DROP COLUMN owner_user_id;
ALTER TABLE agents DROP COLUMN kind;
ALTER TABLE agents ALTER COLUMN team_id SET NOT NULL;
-- Dropping kind dropped the name index (its predicate used kind). Names of
-- agents made by hand stay unique in the team; service-managed agents are
-- found by their external key.
CREATE UNIQUE INDEX agents_team_name_key ON agents (team_id, lower(name)) WHERE external_key IS NULL;
UPDATE agent_templates SET description = 'A general helper with a clock tool: a starting point to deploy and test.' WHERE slug = 'assistant';

-- +goose Down
DROP INDEX agents_team_name_key;
ALTER TABLE agents ALTER COLUMN team_id DROP NOT NULL;
ALTER TABLE agents ADD COLUMN kind text NOT NULL DEFAULT 'team' CHECK (kind IN ('personal', 'team'));
ALTER TABLE agents ADD COLUMN owner_user_id uuid REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE agents ADD CONSTRAINT agents_owner CHECK (
    (kind = 'personal' AND owner_user_id IS NOT NULL AND team_id IS NULL) OR
    (kind = 'team' AND team_id IS NOT NULL AND owner_user_id IS NULL));
ALTER TABLE agents ADD CONSTRAINT agents_external_key_team CHECK (external_key IS NULL OR kind = 'team');
CREATE UNIQUE INDEX agents_personal_key ON agents (owner_user_id) WHERE kind = 'personal';
CREATE UNIQUE INDEX agents_team_name_key ON agents (team_id, lower(name)) WHERE kind = 'team';
