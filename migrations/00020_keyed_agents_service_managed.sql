-- A keyed agent in a team that isn't a personal workspace is service-managed,
-- whoever writes the row.
--
-- 00018 added agents.service_managed with DEFAULT false. During a rolling
-- upgrade (docs/replicas.md) a v0.2 replica still serves
-- PUT /v1/teams/{team}/agents/by-key/{key} after the migration, and its
-- InsertAgent doesn't name the column: the agent it creates for an app's
-- user would be an ordinary team agent, whose conversations the team's people
-- could read and which an owner could move. The trigger makes the database
-- keep the rule (internal/agents sets the same on create): on insert, and on
-- any update of the key, the team or the flag. A move drops the key (and the
-- flag), so it is not affected; a keyed agent of a personal workspace is the
-- workspace's and stays as it was written.

-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION agents_keyed_service_managed() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.external_key IS NOT NULL AND NOT NEW.service_managed
       AND EXISTS (SELECT 1 FROM teams WHERE id = NEW.team_id AND kind <> 'personal') THEN
        NEW.service_managed := true;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER agents_keyed_service_managed
    BEFORE INSERT OR UPDATE OF external_key, team_id, service_managed ON agents
    FOR EACH ROW EXECUTE FUNCTION agents_keyed_service_managed();

-- Agents a v0.2 replica created between 00018 and this migration (keyed, in a
-- shared team, not service-managed). Nothing in v0.3 makes such an agent, so
-- every row this changes is one of those.
UPDATE agents a SET service_managed = true
FROM teams t
WHERE t.id = a.team_id AND t.kind <> 'personal' AND a.external_key IS NOT NULL AND NOT a.service_managed;

-- +goose Down
DROP TRIGGER agents_keyed_service_managed ON agents;
DROP FUNCTION agents_keyed_service_managed();
