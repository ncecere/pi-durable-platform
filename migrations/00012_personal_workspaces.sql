-- Personal workspaces: a one-person team (kind personal) each person gets at
-- sign-in, while platform admins have them turned on.
--
--   * teams.kind: team (shared; created by platform admins) or personal
--     (personal_owner: its only member, owner; one per person). A personal
--     workspace has no other members, invites or service accounts, and
--     deploys catalog templates only.
--   * personal_workspace_settings: one row of platform-wide defaults, set by
--     platform admins: whether people get a workspace, how many agents, how
--     many running at once, and a monthly cost budget (recorded; not
--     enforced yet).
--   * teams.limit_*: one personal workspace's overrides (NULL: the default).
--   * models.personal, mcp_servers.personal: available in personal
--     workspaces (visibility and model_teams decide for shared teams).
--     Existing ones published to every team are available there too.

-- +goose Up
ALTER TABLE teams ADD COLUMN kind text NOT NULL DEFAULT 'team' CHECK (kind IN ('team', 'personal'));
ALTER TABLE teams ADD COLUMN personal_owner uuid UNIQUE REFERENCES users (id) ON DELETE CASCADE;
ALTER TABLE teams ADD CONSTRAINT teams_personal_owner CHECK ((kind = 'personal') = (personal_owner IS NOT NULL));
ALTER TABLE teams ADD COLUMN limit_max_agents integer CHECK (limit_max_agents BETWEEN 0 AND 10000);
ALTER TABLE teams ADD COLUMN limit_max_running integer CHECK (limit_max_running BETWEEN 0 AND 1000);
ALTER TABLE teams ADD COLUMN limit_monthly_budget_usd numeric(12, 2) CHECK (limit_monthly_budget_usd >= 0);

CREATE TABLE personal_workspace_settings (
    id                 boolean        PRIMARY KEY DEFAULT true CHECK (id),
    enabled            boolean        NOT NULL DEFAULT true,
    max_agents         integer        NOT NULL DEFAULT 5 CHECK (max_agents BETWEEN 0 AND 10000),
    max_running        integer        NOT NULL DEFAULT 2 CHECK (max_running BETWEEN 0 AND 1000),
    monthly_budget_usd numeric(12, 2) CHECK (monthly_budget_usd >= 0),
    revision           bigint         NOT NULL DEFAULT 1,
    updated_at         timestamptz    NOT NULL DEFAULT now()
);
INSERT INTO personal_workspace_settings DEFAULT VALUES;

ALTER TABLE models ADD COLUMN personal boolean NOT NULL DEFAULT false;
UPDATE models SET personal = (visibility = 'all');
ALTER TABLE mcp_servers ADD COLUMN personal boolean NOT NULL DEFAULT false;
UPDATE mcp_servers SET personal = (visibility = 'all');

-- +goose Down
ALTER TABLE mcp_servers DROP COLUMN personal;
ALTER TABLE models DROP COLUMN personal;
DROP TABLE personal_workspace_settings;
DELETE FROM teams WHERE kind = 'personal';
ALTER TABLE teams DROP COLUMN limit_monthly_budget_usd;
ALTER TABLE teams DROP COLUMN limit_max_running;
ALTER TABLE teams DROP COLUMN limit_max_agents;
ALTER TABLE teams DROP CONSTRAINT teams_personal_owner;
ALTER TABLE teams DROP COLUMN personal_owner;
ALTER TABLE teams DROP COLUMN kind;
