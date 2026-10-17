-- Services reach agents with keys.
--
--   * audit_log: service accounts become actors (actor_kind service_account,
--     actor_service_account_id; no foreign key, so history survives deleting
--     the account).
--   * agents.external_key: a service's own key for an agent it manages, unique
--     in the team (for example one agent per user of a chat app). Agents with
--     a key are service-managed: only the team's service accounts talk to them.

-- +goose Up
ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_kind_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_kind_check
    CHECK (actor_kind IN ('user', 'api_key', 'service_account', 'system'));
ALTER TABLE audit_log ADD COLUMN actor_service_account_id uuid;

ALTER TABLE agents ADD COLUMN external_key text
    CHECK (external_key ~ '^[A-Za-z0-9._:@-]{1,200}$');
ALTER TABLE agents ADD CONSTRAINT agents_external_key_team CHECK (external_key IS NULL OR kind = 'team');
CREATE UNIQUE INDEX agents_external_key ON agents (team_id, external_key) WHERE external_key IS NOT NULL;

-- +goose Down
DROP INDEX agents_external_key;
ALTER TABLE agents DROP CONSTRAINT agents_external_key_team;
ALTER TABLE agents DROP COLUMN external_key;
ALTER TABLE audit_log DROP COLUMN actor_service_account_id;
ALTER TABLE audit_log DROP CONSTRAINT audit_log_actor_kind_check;
ALTER TABLE audit_log ADD CONSTRAINT audit_log_actor_kind_check CHECK (actor_kind IN ('user', 'api_key', 'system'));
